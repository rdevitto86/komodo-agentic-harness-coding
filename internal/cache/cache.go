// Package cache is the local cache of context pieces and prompt-cache marker plans.
// It is a folder of small JSON files found by a fingerprint of their inputs; it never stores a model's answer.
package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Namespace groups cache entries by the kind of piece they hold.
type Namespace string

// The namespaces a cache holds, one directory each.
const (
	NSSkills  Namespace = "skills"  // skill index and bodies
	NSContext Namespace = "context" // file slices, trims, diffs and briefs
	NSMap     Namespace = "map"     // repository map
	NSSetup   Namespace = "setup"   // repository profile
	NSPrompt  Namespace = "prompt"  // marker plans
)

// Key is the 64-character lowercase hexadecimal fingerprint of an entry's inputs.
type Key string

// DefaultMaxBytes is the entry store cap used when none is given (256 MB).
const DefaultMaxBytes int64 = 256 << 20

// rescanInterval bounds how stale a Put's view of other processes' writes can be, so overshoot stays small without a walk per Put.
const rescanInterval = 30 * time.Second

// ErrEntryTooLarge reports a Put whose entry alone exceeds the cache cap.
var ErrEntryTooLarge = errors.New("cache entry larger than the cache cap")

// ErrClosed reports a Put on a cache that the Runtime Manager has closed.
var ErrClosed = errors.New("cache is closed")

// renameFile moves the temp file into place; tests replace it to simulate rename failures.
var renameFile = os.Rename

// syncFile flushes the temp file before it is renamed; tests replace it to watch the order of steps.
var syncFile = func(f *os.File) error { return f.Sync() }

// syncDirHandle flushes an open directory after a rename; tests replace it to watch the order of steps.
var syncDirHandle = func(d *os.File) error { return d.Sync() }

// Limits are the size caps the Runtime Manager passes in from the Configuration Manager.
type Limits struct {
	MaxBytes     int64  // cap of the entry store, from cache.max_mb
	LangMaxBytes int64  // cap of the language store, from sandbox.lang_cache_gb
	LangRoot     string // folder that holds one language store per repository; TouchLang stamps inside it
}

// NamespaceStats counts the entries and bytes of one namespace.
type NamespaceStats struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
}

// Stats counts one cache in total and per namespace.
type Stats struct {
	Entries    int                          `json:"entries"`    // entries held
	Bytes      int64                        `json:"bytes"`      // size of those entries
	Namespaces map[Namespace]NamespaceStats `json:"namespaces"` // the same counts per namespace
	Hits       map[Namespace]int64          `json:"hits"`       // lookups that found a valid entry
	Lookups    map[Namespace]int64          `json:"lookups"`    // all lookups
}

// Handle is what the Runtime Manager passes to the Context Manager and to each Task Manager.
// It has no Stats, Clear, Close, TrimLang or TouchLang; only the Runtime Manager calls those on the Cache.
type Handle interface {
	// Key hashes the version and inputs into an entry key.
	Key(inputs ...string) Key
	// Get returns the payload under key; a miss, a failed whole-entry check or an I/O error all return false.
	Get(ns Namespace, key Key) ([]byte, bool)
	// Put stores data under key atomically, then evicts least recently read entries until the cache fits its cap.
	Put(ns Namespace, key Key, data []byte) error
}

// Cache is a content-addressed store of derived pieces under one repository's root directory.
type Cache struct {
	root         string
	version      string
	maxBytes     int64
	langMaxBytes int64
	langRoot     string
	now          func() time.Time      // clock for last-read stamps; tests replace it
	closed       atomic.Bool           // set by Close
	hits         counters              // Get calls that found a valid entry
	lookups      counters              // all Get calls
	mu           sync.Mutex            // guards the three fields below
	index        map[string]cacheEntry // path -> entry; nil until first needed
	total        int64                 // sum of sizes in index
	scanned      time.Time             // when index last came from a disk scan
}

// counters holds one atomic count per namespace; the map is built once and never changes.
type counters map[Namespace]*atomic.Int64

// newCounters returns a zero count for every namespace.
func newCounters() counters {
	c := counters{}
	for _, ns := range namespaces {
		c[ns] = new(atomic.Int64)
	}
	return c
}

// handle is the Key, Get and Put view of a Cache; it has no Stats, Clear or Close.
type handle struct{ c *Cache }

// Key hashes the version and inputs into an entry key.
func (h handle) Key(inputs ...string) Key { return h.c.Key(inputs...) }

// Get returns the payload under key, or false on a miss.
func (h handle) Get(ns Namespace, key Key) ([]byte, bool) { return h.c.Get(ns, key) }

// Put stores data under key and evicts if the cache is over its cap.
func (h handle) Put(ns Namespace, key Key, data []byte) error { return h.c.Put(ns, key, data) }

// New opens the cache of one repository at root for a binary version, creating root if needed.
// A cap of zero or less selects its default; only the Runtime Manager calls it.
func New(root, version string, lim Limits) (*Cache, error) {
	if root == "" {
		return nil, errors.New("cache root is empty")
	}
	if lim.MaxBytes <= 0 {
		lim.MaxBytes = DefaultMaxBytes
	}
	if lim.LangMaxBytes <= 0 {
		lim.LangMaxBytes = DefaultLangMaxBytes
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create cache root: %w", err)
	}
	return &Cache{
		root: root, version: version, maxBytes: lim.MaxBytes, langMaxBytes: lim.LangMaxBytes, langRoot: lim.LangRoot,
		now: time.Now, hits: newCounters(), lookups: newCounters(),
	}, nil
}

// Handle returns the cache as a Handle, ready to pass to another component.
func (c *Cache) Handle() Handle { return handle{c: c} }

// Key hashes the inputs together with the binary version into a 64-char hex entry name.
// Inputs are length-prefixed so ("ab","c") and ("a","bc") differ.
func (c *Cache) Key(inputs ...string) Key {
	h := sha256.New()
	var n [8]byte
	for _, in := range append([]string{c.version}, inputs...) {
		binary.BigEndian.PutUint64(n[:], uint64(len(in)))
		h.Write(n[:])
		h.Write([]byte(in))
	}
	return Key(hex.EncodeToString(h.Sum(nil)))
}

// Get returns the payload stored under key, or false on a miss.
// An entry that fails its check is deleted and reported as a miss; I/O errors and a closed cache also miss.
func (c *Cache) Get(ns Namespace, key Key) ([]byte, bool) {
	path, err := c.entryPath(ns, key)
	if err != nil || c.closed.Load() {
		return nil, false
	}
	payload, ok := c.read(ns, path)
	c.lookups[ns].Add(1)
	if ok {
		c.hits[ns].Add(1)
	}
	return payload, ok
}

// read loads and checks the entry file at path, deleting it when the check fails and stamping its last-read time when it holds.
func (c *Cache) read(ns Namespace, path string) ([]byte, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	payload, ok := decodeEntry(raw)
	if !ok {
		_ = os.Remove(path)
		c.forget(path)
		return nil, false
	}
	now := c.now()
	if os.Chtimes(path, now, now) == nil {
		c.track(cacheEntry{ns: ns, path: path, size: int64(len(raw)), used: now}, false)
	}
	return payload, true
}

// Put stores data under key atomically, then evicts least recently read entries until the cache fits its cap.
// Concurrent Puts of one key from any number of processes leave one complete entry.
func (c *Cache) Put(ns Namespace, key Key, data []byte) error {
	if c.closed.Load() {
		return ErrClosed
	}
	path, err := c.entryPath(ns, key)
	if err != nil {
		return err
	}
	raw := encodeEntry(data)
	size := int64(len(raw))
	if size > c.maxBytes {
		return fmt.Errorf("%w: %d bytes against %d", ErrEntryTooLarge, size, c.maxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	if renameFailed, err := writeEntry(path, raw); err != nil {
		// A key names its inputs, so a valid entry already at path is as good as ours.
		if !renameFailed {
			return fmt.Errorf("store cache entry: %w", err)
		}
		existing, ok := validEntrySize(path)
		if !ok {
			return fmt.Errorf("store cache entry: %w", err)
		}
		size = existing
	}
	now := c.now()
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("stamp cache entry: %w", err)
	}
	c.track(cacheEntry{ns: ns, path: path, size: size, used: now}, true)
	return c.evict(path)
}

// writeEntry writes data to a temp file beside path, then renames it over path.
// It reports whether the failure was the final rename; a failure removes the temp file.
func writeEntry(path string, data []byte) (renameFailed bool, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}
	name := tmp.Name()
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(0o644)
	}
	if err == nil {
		err = syncFile(tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return false, fmt.Errorf("write temp file: %w", err)
	}
	if err = renameFile(name, path); err != nil {
		_ = os.Remove(name)
		return true, fmt.Errorf("rename onto %s: %w", path, err)
	}
	if err = syncDir(filepath.Dir(path)); err != nil {
		return false, fmt.Errorf("sync directory of %s: %w", path, err)
	}
	return false, nil
}

// syncDir flushes a directory entry to disk; it is a no-op where the OS cannot sync directories.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := syncDirHandle(d); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

// validEntrySize returns the on-disk size of the entry file at path, or false when it is missing or fails its check.
func validEntrySize(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	if _, ok := decodeEntry(raw); !ok {
		return 0, false
	}
	return int64(len(raw)), true
}

// Stats counts the entries on disk under this cache's root and the lookups served since it opened.
func (c *Cache) Stats() (Stats, error) {
	entries, err := scanCache(c.root)
	if err != nil {
		return Stats{}, err
	}
	s := statsOf(entries)
	for ns, n := range c.lookups {
		if v := n.Load(); v > 0 {
			s.Lookups[ns] = v
		}
	}
	for ns, n := range c.hits {
		if v := n.Load(); v > 0 {
			s.Hits[ns] = v
		}
	}
	return s, nil
}

// Clear deletes every entry of the given namespaces, or of all when none is given, and returns what it removed.
// An unknown namespace or a removal error returns an error and no counts.
func (c *Cache) Clear(ns ...Namespace) (Stats, error) {
	if len(ns) == 0 {
		ns = namespaces
	}
	seen := map[Namespace]bool{}
	var targets []Namespace
	for _, n := range ns {
		if !validNamespace(n) {
			return Stats{}, fmt.Errorf("unknown cache namespace %q", n)
		}
		if !seen[n] {
			seen[n] = true
			targets = append(targets, n)
		}
	}
	entries, err := scanCache(c.root, targets...)
	if err != nil {
		return Stats{}, err
	}
	for _, n := range targets {
		if err := os.RemoveAll(filepath.Join(c.root, string(n))); err != nil {
			return Stats{}, fmt.Errorf("remove namespace %s: %w", n, err)
		}
	}
	c.mu.Lock()
	c.index, c.total = nil, 0
	c.mu.Unlock()
	return statsOf(entries), nil
}

// Close stops the cache; later Get calls miss and later Put calls return ErrClosed. A second call returns nil.
func (c *Cache) Close() error {
	c.closed.Store(true)
	return nil
}

// entryPath maps a namespace and key to its file under a 2-level fan-out, refusing anything that is not a known namespace and a hex hash.
func (c *Cache) entryPath(ns Namespace, key Key) (string, error) {
	if !validNamespace(ns) {
		return "", fmt.Errorf("unknown cache namespace %q", ns)
	}
	if !validKey(key) {
		return "", errors.New("cache key is not a sha256 hex string")
	}
	return filepath.Join(c.root, string(ns), string(key[0:2]), string(key[2:4]), string(key)+".json"), nil
}

// track records an entry in the in-memory index, loading the index from disk first if needed.
// A write replaces any earlier size for the path; a read only refreshes entries already indexed.
func (c *Cache) track(e cacheEntry, write bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.index == nil {
		if !write {
			return
		}
		if c.loadLocked() != nil {
			return // evict retries the load and reports the failure
		}
	}
	if old, ok := c.index[e.path]; ok {
		c.total -= old.size
	}
	c.index[e.path] = e
	c.total += e.size
}

// forget drops a path from the in-memory index.
func (c *Cache) forget(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.index[path]; ok {
		c.total -= old.size
		delete(c.index, path)
	}
}

// loadLocked rebuilds the index from disk; the caller holds c.mu.
// On a scan error the index stays nil so the next call retries.
func (c *Cache) loadLocked() error {
	entries, err := scanCache(c.root)
	if err != nil {
		c.index, c.total = nil, 0
		return err
	}
	c.index, c.total = map[string]cacheEntry{}, 0
	for _, e := range entries {
		c.index[e.path] = e
		c.total += e.size
	}
	c.scanned = c.now()
	return nil
}

// evict deletes least recently read entries, never keep, until the total fits the cap.
// It rescans the disk first, when over the cap or when the last scan is old, so other processes' entries count.
func (c *Cache) evict(keep string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.index != nil && c.total <= c.maxBytes && c.now().Sub(c.scanned) < rescanInterval {
		return nil
	}
	if err := c.loadLocked(); err != nil {
		return err
	}
	var victims []cacheEntry
	for _, e := range c.index {
		if e.path != keep {
			victims = append(victims, e)
		}
	}
	sort.Slice(victims, func(i, j int) bool {
		if !victims[i].used.Equal(victims[j].used) {
			return victims[i].used.Before(victims[j].used)
		}
		return victims[i].path < victims[j].path
	})
	for _, v := range victims {
		if c.total <= c.maxBytes {
			break
		}
		if err := os.Remove(v.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("evict cache entry: %w", err)
		}
		c.total -= v.size
		delete(c.index, v.path)
	}
	return nil
}
