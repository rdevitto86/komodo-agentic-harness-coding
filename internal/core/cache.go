package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Namespace groups cache entries by the kind of result they hold.
type Namespace string

// The namespaces a cache root holds, one directory each.
const (
	NSSkills  Namespace = "skills"  // skill index and bodies
	NSContext Namespace = "context" // file slices, trims, diffs and briefs
	NSChecks  Namespace = "checks"  // check results and test baselines
	NSReview  Namespace = "review"  // review findings
	NSSetup   Namespace = "setup"   // dependency setup results
	NSMap     Namespace = "map"     // repo map and outline lint
)

// namespaces lists every valid namespace.
var namespaces = []Namespace{NSSkills, NSContext, NSChecks, NSReview, NSSetup, NSMap}

// DefaultCacheMaxBytes is the per-repo cap used when none is given (256 MB).
const DefaultCacheMaxBytes int64 = 256 << 20

// entryHeaderLen is the size of the hex payload hash plus newline that starts every entry file.
const entryHeaderLen = 2*sha256.Size + 1

// staleTempAge is how old a leftover temp file must be before a scan deletes it.
const staleTempAge = time.Hour

// ErrCacheEntryTooLarge reports a Put whose entry alone exceeds the cache cap.
var ErrCacheEntryTooLarge = errors.New("cache entry larger than the cache cap")

// NamespaceStats counts the entries and bytes of one namespace.
type NamespaceStats struct {
	Entries int   // number of entry files
	Bytes   int64 // on-disk size of those files
}

// CacheStats counts a whole cache root, in total and per namespace.
type CacheStats struct {
	Entries    int                          // number of entry files
	Bytes      int64                        // on-disk size of those files
	Namespaces map[Namespace]NamespaceStats // the same counts per namespace
}

// cacheEntry is one entry file as seen by a directory scan.
type cacheEntry struct {
	ns   Namespace
	path string
	size int64
	used time.Time // last read, kept in the file's mtime
}

// Cache is a content-addressed store of derived results under one root directory.
// Never cache a nondeterministic result such as a build response; callers own that rule.
type Cache struct {
	root     string
	version  string
	maxBytes int64
	now      func() time.Time // clock for last-used stamps; tests replace it
	mu       sync.Mutex
	index    map[string]cacheEntry // path -> entry; nil until first needed
	total    int64                 // sum of sizes in index
}

// NewCache opens a cache at root for the given binary version, creating the root if needed.
// A maxBytes of zero or less selects DefaultCacheMaxBytes.
func NewCache(root, version string, maxBytes int64) (*Cache, error) {
	if root == "" {
		return nil, errors.New("cache root is empty")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultCacheMaxBytes
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create cache root: %w", err)
	}
	return &Cache{root: root, version: version, maxBytes: maxBytes, now: time.Now}, nil
}

// Key hashes the inputs together with the binary version into a 64-char hex entry name.
// Inputs are length-prefixed so ("ab","c") and ("a","bc") differ.
func (c *Cache) Key(inputs ...string) string {
	h := sha256.New()
	var n [8]byte
	for _, in := range append([]string{c.version}, inputs...) {
		binary.BigEndian.PutUint64(n[:], uint64(len(in)))
		h.Write(n[:])
		h.Write([]byte(in))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the payload stored under key, or false on a miss.
// An entry whose hash does not match is deleted and reported as a miss; I/O errors also miss.
func (c *Cache) Get(ns Namespace, key string) ([]byte, bool) {
	path, err := c.entryPath(ns, key)
	if err != nil {
		return nil, false
	}
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
func (c *Cache) Put(ns Namespace, key string, data []byte) error {
	path, err := c.entryPath(ns, key)
	if err != nil {
		return err
	}
	size := int64(len(data)) + entryHeaderLen
	if size > c.maxBytes {
		return fmt.Errorf("%w: %d bytes against %d", ErrCacheEntryTooLarge, size, c.maxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	if err := WriteFileAtomic(path, encodeEntry(data), 0o644); err != nil {
		return fmt.Errorf("store cache entry: %w", err)
	}
	now := c.now()
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("stamp cache entry: %w", err)
	}
	c.track(cacheEntry{ns: ns, path: path, size: size, used: now}, true)
	return c.evict(path)
}

// Stats counts the entries on disk under this cache's root.
func (c *Cache) Stats() (CacheStats, error) {
	return CacheStatsOf(c.root)
}

// Clear deletes every entry and returns what it removed.
func (c *Cache) Clear() (CacheStats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := scanCache(c.root)
	if err != nil {
		return CacheStats{}, err
	}
	for _, ns := range namespaces {
		if err := os.RemoveAll(filepath.Join(c.root, string(ns))); err != nil {
			return CacheStats{}, fmt.Errorf("remove namespace %s: %w", ns, err)
		}
	}
	c.index, c.total = nil, 0
	return statsOf(entries), nil
}

// CacheStatsOf counts the entries under a cache root; a missing root counts as empty.
func CacheStatsOf(root string) (CacheStats, error) {
	entries, err := scanCache(root)
	if err != nil {
		return CacheStats{}, err
	}
	return statsOf(entries), nil
}

// SumCacheStats adds several roots' stats into one machine-wide total.
func SumCacheStats(all ...CacheStats) CacheStats {
	sum := CacheStats{Namespaces: map[Namespace]NamespaceStats{}}
	for _, s := range all {
		sum.Entries += s.Entries
		sum.Bytes += s.Bytes
		for ns, n := range s.Namespaces {
			cur := sum.Namespaces[ns]
			cur.Entries += n.Entries
			cur.Bytes += n.Bytes
			sum.Namespaces[ns] = cur
		}
	}
	return sum
}

// entryPath maps a namespace and key to its file under a 2-level fan-out, refusing anything that is not a known namespace and a hex hash.
func (c *Cache) entryPath(ns Namespace, key string) (string, error) {
	if !validNamespace(ns) {
		return "", fmt.Errorf("unknown cache namespace %q", ns)
	}
	if !validKey(key) {
		return "", errors.New("cache key is not a sha256 hex string")
	}
	return filepath.Join(c.root, string(ns), key[0:2], key[2:4], key), nil
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
		c.loadLocked()
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
func (c *Cache) loadLocked() {
	c.index, c.total = map[string]cacheEntry{}, 0
	entries, _ := scanCache(c.root)
	for _, e := range entries {
		c.index[e.path] = e
		c.total += e.size
	}
}

// evict deletes least recently read entries, never keep, until the total fits the cap.
// It rescans the disk first so entries other processes wrote or read count.
func (c *Cache) evict(keep string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total <= c.maxBytes {
		return nil
	}
	c.loadLocked()
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

// encodeEntry prefixes the payload with its hex sha256 and a newline.
func encodeEntry(payload []byte) []byte {
	sum := sha256.Sum256(payload)
	out := make([]byte, 0, entryHeaderLen+len(payload))
	out = hex.AppendEncode(out, sum[:])
	out = append(out, '\n')
	return append(out, payload...)
}

// decodeEntry returns the payload of an entry file, or false when the header is missing or the hash does not match.
func decodeEntry(raw []byte) ([]byte, bool) {
	if len(raw) < entryHeaderLen || raw[entryHeaderLen-1] != '\n' {
		return nil, false
	}
	payload := raw[entryHeaderLen:]
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])
	if !bytes.Equal(raw[:entryHeaderLen-1], []byte(want)) {
		return nil, false
	}
	return payload, true
}

// validNamespace reports whether ns is one of the known namespaces.
func validNamespace(ns Namespace) bool {
	for _, n := range namespaces {
		if n == ns {
			return true
		}
	}
	return false
}

// validKey reports whether key is 64 lowercase hex characters.
func validKey(key string) bool {
	if len(key) != 2*sha256.Size {
		return false
	}
	for _, r := range key {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// scanCache lists every entry file under root and deletes stale temp files left by crashed writers.
func scanCache(root string) ([]cacheEntry, error) {
	var out []cacheEntry
	for _, ns := range namespaces {
		dir := filepath.Join(root, string(ns))
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil // vanished between listing and stat
			}
			switch {
			case validKey(d.Name()):
				out = append(out, cacheEntry{ns: ns, path: path, size: info.Size(), used: info.ModTime()})
			case strings.Contains(d.Name(), ".tmp-") && time.Since(info.ModTime()) > staleTempAge:
				_ = os.Remove(path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan cache namespace %s: %w", ns, err)
		}
	}
	return out, nil
}

// statsOf totals a scan overall and per namespace.
func statsOf(entries []cacheEntry) CacheStats {
	s := CacheStats{Namespaces: map[Namespace]NamespaceStats{}}
	for _, e := range entries {
		s.Entries++
		s.Bytes += e.size
		n := s.Namespaces[e.ns]
		n.Entries++
		n.Bytes += e.size
		s.Namespaces[e.ns] = n
	}
	return s
}
