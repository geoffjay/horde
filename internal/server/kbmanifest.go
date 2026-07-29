package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// kbManifestCache holds the last-scanned manifest for a scope, keyed by the
// canonical tree path. The cache is invalidated by a tree-wide signature
// (max mtime + file count): a scan is only re-run when the latest mtime
// across all files and subdirectories, or the total file count, has changed
// since the last scan. This detects in-place edits at any depth (directory
// mtime alone misses content changes) and makes the steady-state GET
// manifest (If-None-Match hit) nearly free.
type kbManifestCache struct {
	mu    sync.Mutex
	scans map[string]*kbCachedManifest
}

// kbCachedManifest is a cached manifest scan for one tree, with the tree-wide
// signature at scan time (the cache invalidation key): the latest mtime across
// all files and subdirectories, plus the total file count.
type kbCachedManifest struct {
	manifest  *KBManifest
	maxMtime  time.Time
	fileCount int
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
	data, err := os.ReadFile(path) //#nosec G304 // path is KB-internal, validated by ValidateKBPath or derived from a WalkDir of the KB root
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return kbDigestPrefix + hex.EncodeToString(h[:]), nil
}

// cachedScan returns a cached manifest for the given tree, re-scanning only
// when the tree-wide signature (max mtime across all files and subdirectories
// + total file count) has changed since the last scan. This detects in-place
// edits at any depth — directory mtime alone misses content changes — and
// makes the steady-state poll (If-None-Match hit) nearly free.
func (c *kbManifestCache) cachedScan(root string, policy KBScopePolicy, authority string, scope KBScopeRef) (*KBManifest, error) {
	maxMtime, fileCount, err := kbTreeSignature(root)
	if err != nil {
		return nil, fmt.Errorf("stat kb root: %w", err)
	}

	c.mu.Lock()
	cached, ok := c.scans[root]
	c.mu.Unlock()

	if ok && cached.maxMtime.Equal(maxMtime) && cached.fileCount == fileCount {
		return cached.manifest, nil
	}

	manifest, err := scanManifest(root, policy, authority, scope)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.scans[root] = &kbCachedManifest{manifest: manifest, maxMtime: maxMtime, fileCount: fileCount}
	c.mu.Unlock()

	return manifest, nil
}

// kbTreeSignature walks the tree and returns the latest mtime across all
// files and subdirectories plus the total file count. This is the cache
// invalidation key: if either value changes, the manifest is re-scanned.
// Unlike the root-directory mtime alone, this detects in-place content edits
// at any depth (KSP §2.5).
func kbTreeSignature(root string) (time.Time, int, error) {
	var maxMtime time.Time
	var fileCount int

	walkErr := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // walk continues past the errored entry
		}
		if info.ModTime().After(maxMtime) {
			maxMtime = info.ModTime()
		}
		if info.Mode().IsRegular() {
			fileCount++
		}
		return nil
	})
	if walkErr != nil {
		return time.Time{}, 0, walkErr
	}

	return maxMtime, fileCount, nil
}

// invalidate removes the cached manifest for a tree, forcing the next scan to
// re-read. Called after a write lands or when the watcher fires.
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
	cleaned, err := ValidateKBPath(relPath)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	full := filepath.Join(root, cleaned)

	// Reject symlinks: Lstat does not follow them, so a symlink inside the
	// KB tree is caught here (KSP §2.2, §10). A symlink pointing outside the
	// root would otherwise leak arbitrary file bytes.
	info, err := os.Lstat(full)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		return nil, "", time.Time{}, fmt.Errorf("not a regular file: %s", relPath)
	}

	// Resolve symlinks on the full path and verify the resolved path is
	// still inside the root (defense in depth — Lstat already rejected the
	// direct symlink case, but a component of the path could be a symlink).
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if !isPathInsideRoot(resolved, root) {
		return nil, "", time.Time{}, fmt.Errorf("kb: path escapes root: %s", relPath)
	}

	data, err := os.ReadFile(full) //#nosec G304 // full is validated inside root by ValidateKBPath + EvalSymlinks + isPathInsideRoot
	if err != nil {
		return nil, "", time.Time{}, err
	}

	digest, err := hashFile(full)
	if err != nil {
		return nil, "", time.Time{}, err
	}

	return data, digest, info.ModTime().UTC(), nil
}

// isPathInsideRoot reports whether resolved is contained within root after
// both are cleaned and made absolute. Used after EvalSymlinks to verify a
// resolved path did not escape the KB root (KSP §2.2, §10).
func isPathInsideRoot(resolved, root string) bool {
	// Resolve symlinks on root too — on macOS /tmp is a symlink to
	// /private/tmp, so an un-resolved root would fail the containment check
	// against a resolved file path.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		resolvedRoot = root // fallback to the un-resolved root
	}
	absRoot, err := filepath.Abs(filepath.Clean(resolvedRoot))
	if err != nil {
		return false
	}
	absResolved, err := filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absResolved)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, "../")
}

// KBFindEntry looks up an entry in a manifest by path. Returns nil when the
// path is not in the manifest (KSP §4.2: 404). Used by the file handler to
// verify a path is in the serving manifest before reading.
func KBFindEntry(m *KBManifest, path string) *KBEntry {
	cleaned, err := ValidateKBPath(path)
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
