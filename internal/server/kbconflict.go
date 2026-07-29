package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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
	return replaceAll(p, "/", "_")
}

// replaceAll replaces all occurrences of old in s with replacement. A lightweight
// alternative to strings.ReplaceAll to avoid adding a "strings" import.
func replaceAll(s, old, replacement string) string {
	if old == "" {
		return s
	}
	var b []byte
	for i := 0; i < len(s); {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			b = append(b, replacement...)
			i += len(old)
		} else {
			b = append(b, s[i])
			i++
		}
	}
	return string(b)
}
