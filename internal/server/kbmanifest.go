package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// kbManifestCache holds the last-scanned manifest for a scope, keyed by the
// canonical tree path. The cache is invalidated by mtime comparison: a scan
// is only re-run when the root's modtime has changed since the last scan.
// This makes the steady-state GET manifest (If-None-Match hit) nearly free.
type kbManifestCache struct {
	mu    sync.Mutex
	scans map[string]*kbCachedManifest
}

// kbCachedManifest is a cached manifest scan for one tree, with the root's
// modtime at scan time (the cache invalidation key).
type kbCachedManifest struct {
	manifest  *KBManifest
	rootMtime time.Time
}

// newKBManifestCache creates an empty manifest cache.
func newKBManifestCache() *kbManifestCache {
	return &kbManifestCache{scans: make(map[string]*kbCachedManifest)}
}

// scanManifest scans a canonical tree and produces a complete manifest (KSP
// §2.5). It walks the tree, hashes each regular file, and builds sorted
// (path, digest) entries. The manifest_digest is a digest over the sorted
// (path, digest) pairs — the cheap change-detection primitive.
//
// Files matching the ignore globs are skipped. Files over the size cap are
// skipped (and recorded as unsynced-with-reason per KSP §5.4 — though the
// manifest simply omits them; the authority's policy is the authority's
// alone). Symlinks are never followed.
func scanManifest(root string, policy KBScopePolicy, authority string, scope KBScopeRef) (*KBManifest, error) {
	ignore := policy.Ignore
	if len(ignore) == 0 {
		ignore = kbIgnoreGlobs()
	}

	var entries []KBEntry
	maxSize := policy.MaxFileSize
	if maxSize == 0 {
		maxSize = DefaultKBMaxFileSize
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type() == fs.ModeSymlink {
			// Never follow symlinks in the KB tree (KSP §2.2).
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		if kbMatchesIgnore(rel, ignore) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if !isKBFile(info) {
			return nil
		}
		if info.Size() > maxSize {
			return nil
		}

		digest, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", rel, err)
		}

		entries = append(entries, KBEntry{
			Path:     rel,
			Digest:   digest,
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan kb tree: %w", walkErr)
	}

	// Sort by path for deterministic manifest_digest.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	manifestDigest := computeManifestDigest(entries)

	return &KBManifest{
		Scope:          scope,
		ManifestDigest: manifestDigest,
		Authority:      authority,
		Policy:         policy,
		Files:          entries,
	}, nil
}

// computeManifestDigest computes a digest over the sorted (path, digest) pairs.
// This is the cheap change-detection primitive: equal digest ⇒ nothing to do
// (KSP §2.5). The digest is over "path\0digest\n" for each entry, which is
// unambiguous and deterministic.
func computeManifestDigest(entries []KBEntry) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\n", e.Path, e.Digest)
	}
	return kbDigestPrefix + hex.EncodeToString(h.Sum(nil))
}

// hashFile reads and SHA-256 hashes a file, returning "sha256:<hex>".
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path) //#nosec G304 // path is KB-internal, validated by kbValidatePath or derived from a WalkDir of the KB root
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return kbDigestPrefix + hex.EncodeToString(h[:]), nil
}

// cachedScan returns a cached manifest for the given tree, re-scanning only
// when the root's modtime has changed since the last scan. This makes the
// steady-state poll (If-None-Match hit) nearly free — the scan is only re-run
// when the tree actually changed.
func (c *kbManifestCache) cachedScan(root string, policy KBScopePolicy, authority string, scope KBScopeRef) (*KBManifest, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat kb root: %w", err)
	}
	rootMtime := info.ModTime()

	c.mu.Lock()
	cached, ok := c.scans[root]
	c.mu.Unlock()

	if ok && cached.rootMtime.Equal(rootMtime) {
		return cached.manifest, nil
	}

	manifest, err := scanManifest(root, policy, authority, scope)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.scans[root] = &kbCachedManifest{manifest: manifest, rootMtime: rootMtime}
	c.mu.Unlock()

	return manifest, nil
}

// invalidate removes the cached manifest for a tree, forcing the next scan to
// re-read. Called after a write lands (slice 4) or when the watcher fires.
func (c *kbManifestCache) invalidate(root string) {
	c.mu.Lock()
	delete(c.scans, root)
	c.mu.Unlock()
}

// kbReadFile reads a file from the KB tree, returning its bytes, digest, and
// modtime. The path is validated before reading (KSP §10).
//
//nolint:gocritic // unnamedResult: results are clear from context
func kbReadFile(root, relPath string) ([]byte, string, time.Time, error) {
	cleaned, err := kbValidatePath(relPath)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	full := filepath.Join(root, cleaned)

	info, err := os.Stat(full)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if !isKBFile(info) {
		return nil, "", time.Time{}, fmt.Errorf("not a regular file: %s", relPath)
	}

	data, err := os.ReadFile(full) //#nosec G304 // full is filepath.Join(root, cleaned) where cleaned passed kbValidatePath
	if err != nil {
		return nil, "", time.Time{}, err
	}

	digest, err := hashFile(full)
	if err != nil {
		return nil, "", time.Time{}, err
	}

	return data, digest, info.ModTime().UTC(), nil
}

// kbFindEntry looks up an entry in a manifest by path. Returns nil when the
// path is not in the manifest (KSP §4.2: 404). Used by the file handler to
// verify a path is in the serving manifest before reading.
//
//nolint:unused // used by kbmanifest_test.go (lint run.tests=false) + slice 4
func kbFindEntry(m *KBManifest, path string) *KBEntry {
	cleaned, err := kbValidatePath(path)
	if err != nil {
		return nil
	}
	// Binary search (entries are sorted by path).
	idx := sort.Search(len(m.Files), func(i int) bool {
		return m.Files[i].Path >= cleaned
	})
	if idx < len(m.Files) && m.Files[idx].Path == cleaned {
		return &m.Files[idx]
	}
	return nil
}
