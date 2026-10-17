package cache

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// staleTempAge is how old a leftover temp file must be before a scan deletes it.
const (
	staleTempAge = time.Hour
	tempMarker   = ".json.tmp-" // tempMarker is the part of a temporary file's name that marks it as an unfinished write.
)

// cacheEntry is one entry file as seen by a directory scan.
type cacheEntry struct {
	ns   Namespace
	path string
	size int64
	used time.Time // last read, kept in the file's mtime
}

// scanCache lists every entry file of the given namespaces (all when none) and deletes stale temp files left by crashed writers.
func scanCache(root string, nss ...Namespace) ([]cacheEntry, error) {
	if len(nss) == 0 {
		nss = namespaces
	}

	var out []cacheEntry
	for _, ns := range nss {
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
			name := d.Name()
			switch {
			case strings.HasSuffix(name, ".json") && validKey(Key(strings.TrimSuffix(name, ".json"))):
				out = append(out, cacheEntry{ns: ns, path: path, size: info.Size(), used: info.ModTime()})
			case strings.Contains(name, tempMarker) && time.Since(info.ModTime()) > staleTempAge:
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

// statsOf totals a scan overall and per namespace, with empty counters.
func statsOf(entries []cacheEntry) Stats {
	s := Stats{
		Namespaces: map[Namespace]NamespaceStats{},
		Hits:       map[Namespace]int64{},
		Lookups:    map[Namespace]int64{},
	}
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
