package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// kbConflictArea manages the node-local conflict directory where dirty local
// files are preserved before being overwritten by convergence (KSP §6.1).
//
// The conflict area is OUTSIDE the knowledgebase tree — a sidecar inside it
// would itself be synced, propagating one node's conflict to the whole cluster
// and overwriting prior preserved copies. Conflict copies are uniquely named
// (path + timestamp + short digest) so repeated conflicts on one path never
// clobber each other (KSP §6.1).
type kbConflictArea struct {
	dir string // conflict dir; empty = disabled (no persistence)
}

// newKBConflictArea creates a conflict area under <dataDir>/kb-conflicts/.
// Empty dataDir = disabled (conflict preservation is skipped, logged only).
func newKBConflictArea(dataDir string) *kbConflictArea {
	if dataDir == "" {
		return &kbConflictArea{dir: ""}
	}
	return &kbConflictArea{dir: filepath.Join(dataDir, "kb-conflicts")}
}

// Preserve copies a dirty local file to the conflict area before convergence
// overwrites it (KSP §5.2, §6.1). The copy is uniquely named by path + timestamp
// + short digest so repeated conflicts never clobber. Returns the conflict copy
// path, or an error if the area is disabled or the copy fails.
func (ca *kbConflictArea) Preserve(scope KBScopeRef, relPath, localPath string) (string, error) {
	if ca.dir == "" {
		return "", fmt.Errorf("conflict area disabled (no data dir)")
	}

	// Read the local file.
	data, err := os.ReadFile(localPath) //#nosec G304 // localPath is within the node's KB tree
	if err != nil {
		return "", fmt.Errorf("read local file for conflict: %w", err)
	}

	// Compute a short digest for the filename.
	h := sha256.Sum256(data)
	shortDigest := hex.EncodeToString(h[:4]) // 8 hex chars

	// Build the unique conflict copy name: <path-escaped>_<timestamp>_<digest>
	// The path is flattened with the scope so conflicts from different scopes
	// never collide.
	ts := time.Now().UTC().Format("20060102T150405Z")
	name := fmt.Sprintf("%s_%s_%s_%s", scope.Kind, scope.ID, escapePath(relPath), ts)
	// Append the short digest to guarantee uniqueness for repeated conflicts.
	name = fmt.Sprintf("%s_%s", name, shortDigest)

	conflictPath := filepath.Join(ca.dir, scope.Kind, scope.ID, name)
	if err := os.MkdirAll(filepath.Dir(conflictPath), kbDirPerm); err != nil { //#nosec G703 // conflictPath is within the node's conflict area
		return "", fmt.Errorf("create conflict dir: %w", err)
	}

	if err := os.WriteFile(conflictPath, data, kbFilePerm); err != nil { //#nosec G306,G703 // standard KB file permissions, conflictPath is within the node's conflict area
		return "", fmt.Errorf("write conflict copy: %w", err)
	}

	return conflictPath, nil
}

// PreserveMissing is called when a local file was deleted locally but the
// authority still has it (KSP §5.1: "deleted locally" row). There is no local
// content to preserve — the file is already gone — so this is a no-op that
// just logs. It exists to make the conflict-handling interface uniform.
func (ca *kbConflictArea) PreserveMissing(_ KBScopeRef, _ string) {
	// Nothing to preserve — the file is already deleted locally.
}

// escapePath replaces path separators with underscores so a conflict copy
// filename is flat (no subdirectories).
func escapePath(p string) string {
	return strings.ReplaceAll(p, "/", "_")
}

// KBConflictEntry describes one preserved conflict copy.
type KBConflictEntry struct {
	Scope   KBScopeRef `json:"scope"`
	Path    string     `json:"path"`    // the KB-relative path that conflicted
	Created time.Time  `json:"created"` // when the conflict copy was preserved
	Digest  string     `json:"digest"`  // short digest from the filename
	File    string     `json:"file"`    // the conflict copy filename
}

// List returns all preserved conflict copies for a scope (KSP §6.1: conflicts
// MUST be surfaced to the operator). Walks the scope's subdirectory under the
// conflict area. Returns nil when the conflict area is disabled.
func (ca *kbConflictArea) List(scope KBScopeRef) ([]KBConflictEntry, error) {
	if ca.dir == "" {
		return nil, nil
	}
	scopeDir := filepath.Join(ca.dir, scope.Kind, scope.ID)
	entries, err := os.ReadDir(scopeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read conflict dir: %w", err)
	}
	var result []KBConflictEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ce := parseConflictFilename(e.Name(), scope, info.ModTime())
		if ce != nil {
			result = append(result, *ce)
		}
	}
	return result, nil
}

// parseConflictFilename parses a conflict copy filename back into an entry.
// The filename format is: <kind>_<id>_<escaped-path>_<timestamp>_<shortdigest>.
func parseConflictFilename(name string, scope KBScopeRef, modTime time.Time) *KBConflictEntry {
	// The kind and id are the first two underscore-separated fields. The
	// rest is escaped-path_timestamp_digest, where the timestamp is
	// RFC3339-ish (20060102T150405Z) and the digest is 8 hex chars.
	rest := name
	// Strip the leading "<kind>_<id>_" prefix.
	prefix := scope.Kind + "_" + scope.ID + "_"
	if !strings.HasPrefix(rest, prefix) {
		return nil
	}
	rest = strings.TrimPrefix(rest, prefix)
	// The remaining is <escaped-path>_<timestamp>_<digest>. Split from the
	// right: the last field is the digest, the second-to-last is the
	// timestamp, and everything before is the escaped path.
	parts := strings.Split(rest, "_")
	if len(parts) < 3 { //nolint:mnd // min fields: escaped-path + timestamp + digest
		return nil
	}
	digest := parts[len(parts)-1]
	ts := parts[len(parts)-2]
	escapedPath := strings.Join(parts[:len(parts)-2], "_")
	// Unescape the path (underscores back to slashes).
	relPath := unescapePath(escapedPath)
	created, err := time.Parse("20060102T150405Z", ts)
	if err != nil {
		created = modTime // fall back to file mtime
	}
	return &KBConflictEntry{
		Scope:   scope,
		Path:    relPath,
		Created: created,
		Digest:  digest,
		File:    name,
	}
}

// unescapePath reverses escapePath, converting underscores back to slashes.
// This is lossy if the original path contained underscores, but the conflict
// copy is a preserved artifact, not a canonical record.
func unescapePath(p string) string {
	return strings.ReplaceAll(p, "_", "/")
}
