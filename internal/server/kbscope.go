package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// KBSyncConfig is the server-layer knowledgebase sync configuration. It mirrors
// config.KBSyncConfig minus config-only concerns (e.g. string durations are
// pre-parsed). The zero value is disabled — no watcher, no convergence, no KB
// routes — preserving the pre-KSP behavior for existing callers and tests.
type KBSyncConfig struct {
	// Enabled is the opt-in switch for KSP synchronization.
	Enabled bool
	// WatchLocal is the local-edit switch: when true a participant watches its
	// own tree and pushes local file edits to the authority. Default false
	// (a read-only participant converges and reads only).
	WatchLocal bool
	// WorkspaceRoot is the node-local root for participant KB materialization.
	// Empty defaults to <data_dir>/workspaces.
	WorkspaceRoot string
	// PollInterval is how often a participant polls the authority's manifest.
	PollInterval time.Duration
	// Debounce is the watcher debounce window.
	Debounce time.Duration
	// MaxFileSize is the per-file size cap in bytes.
	MaxFileSize int64
	// Ignore are KB-root-relative globs to skip.
	Ignore []string
	// PushUser is the horde user id a stage-2 watcher push is attributed to
	// when the edit has no originating API user (a filesystem edit has no
	// logged-in user). Echoed as X-Horde-User on the push so the authority
	// applies the scope's write authority (KSP §9). Empty ⇒ machine pushes
	// carry no write identity and fail closed once per-user auth is enabled.
	PushUser string
}

// KB scope kinds registered by the host. v1 registers only "project"; reserved
// kinds ("team", "user", "cluster") are rejected with 404 until a host defines
// their four bindings (KSP §12.1).
const (
	kbScopeKindProject = "project"
)

// kbForwardedUserHeader is the cross-node identity echo honored on a node
// principal's KB write (KSP §9). Mirrors internal/api's xHordeUserHeader; a
// node principal only resolves when the cluster token matched, so an external
// client cannot forge it.
const kbForwardedUserHeader = "X-Horde-User"

// DefaultKBMaxFileSize is the default per-file size cap (1 MiB) when the
// config does not set one.
const DefaultKBMaxFileSize = 1048576

// KBScopePolicy is the authority's size and ignore policy, published on the
// manifest (KSP §2.5) so a node can detect a mismatched local config.
type KBScopePolicy struct {
	MaxFileSize int64    `json:"max_file_size"`
	Ignore      []string `json:"ignore"`
}

// KBEntry is one file in the canonical tree (KSP §2.3). Identity is the content
// digest — there is deliberately no version counter.
type KBEntry struct {
	Path     string    `json:"path"`
	Digest   string    `json:"digest"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Author   string    `json:"author,omitempty"`
}

// KBManifest is the complete set of entries for one scope's canonical tree,
// plus the policy a node needs (KSP §2.5). ManifestDigest is a digest over
// the sorted (path, digest) pairs — the cheap change-detection primitive.
type KBManifest struct {
	Scope          KBScopeRef    `json:"scope"`
	ManifestDigest string        `json:"manifest_digest"`
	Authority      string        `json:"authority"`
	Policy         KBScopePolicy `json:"policy"`
	Files          []KBEntry     `json:"files"`
}

// KBScopeRef identifies a scope (KSP §2.1): a kind + id naming exactly one
// canonical tree.
type KBScopeRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ScopeResolver binds a knowledgebase scope kind to the host. Everything else
// in the converger is kind-independent. v1 has one implementation:
// projectScope. Registering a second kind later touches nothing else (KSP §12).
type ScopeResolver interface {
	// Kind returns the scope kind string (e.g. "project").
	Kind() string
	// Validate checks whether the id denotes a real scope. Returns nil when
	// the scope exists; a non-nil error (mapped to 404) when it does not.
	Validate(id string) error
	// IsAuthority reports whether this node holds the canonical tree for the
	// given scope. For project scope this is the cluster leader.
	IsAuthority(id string) bool
	// AuthorityTree returns the canonical tree path on the authority, or an
	// error if this node is not the authority.
	AuthorityTree(id string) (string, error)
	// LocalTree returns the node-local materialization path for the scope.
	LocalTree(id string) (string, error)
	// Participates reports whether this node syncs the scope at all.
	Participates(id string) bool
	// Authorize checks whether the request principal may read (w=false) or
	// write (w=true) the scope. KB routes MUST NOT inherit the host's open-reads
	// policy — document bytes cannot be redacted (KSP §9).
	Authorize(r *http.Request, id string, w bool) error
	// Policy returns the authority's size and ignore policy for the scope.
	Policy() KBScopePolicy
	// CachedManifest returns a cached or freshly-scanned manifest for the
	// scope's canonical tree (authority) or local tree (participant).
	CachedManifest(scope KBScopeRef) (*KBManifest, error)
	// ReadFile reads a file from the serving tree (authority or local) for
	// the given scope id.
	ReadFile(id, relPath string) ([]byte, string, time.Time, error)
	// InvalidateCache forces the next manifest scan to re-read the tree for
	// the given scope id.
	InvalidateCache(id string)
	// WriteFile writes a file to the canonical tree (authority only) via
	// temp+rename, returning the new digest. Per-path serialization is the
	// caller's responsibility.
	WriteFile(id, relPath string, data []byte) (digest string, err error)
	// DeleteFile removes a file from the canonical tree (authority only).
	// Returns ErrKBFileNotFound if the path doesn't exist.
	DeleteFile(id, relPath string) error
}

// projectScope implements ScopeResolver for the project kind. The authority is
// the cluster leader (static coordinator or raft-elected). The canonical tree is
// <Project.Workspace>/.horde/knowledgebase/. Authorization is the project's
// own view/write authority — owner, admin, or team member for reads; the
// host's project write authority for writes.
type projectScope struct {
	srv *Server
}

// newProjectScope creates the project scope resolver bound to the server.
func newProjectScope(srv *Server) *projectScope {
	return &projectScope{srv: srv}
}

func (ps *projectScope) Kind() string { return kbScopeKindProject }

// Validate checks whether the id is a known project. Returns a sentinel error
// the handler maps to 404.
//
// On the authority the local project store is the source of truth. On a
// participant the local store is empty (project API requests forward to the
// coordinator), so a scope is "known" once convergence has materialized its local
// tree on disk — which is exactly when the participant can serve local reads
// (KSP §3.2, §4.1).
func (ps *projectScope) Validate(id string) error {
	_, err := ps.srv.projects.Get(id)
	if err == nil {
		return nil
	}
	if !ps.IsAuthority(id) {
		if tree, terr := ps.LocalTree(id); terr == nil {
			if fi, serr := os.Stat(tree); serr == nil && fi.IsDir() {
				return nil
			}
		}
	}
	return err
}

// IsAuthority reports whether this node is the cluster leader (the project
// scope's authority). Without failover the role is static; with raft failover
// the current raft leader is the authority.
func (ps *projectScope) IsAuthority(_ string) bool {
	return ps.srv.isCoordinator()
}

// AuthorityTree returns the canonical KB path for the project on this node
// (the authority): <Project.Workspace>/.horde/knowledgebase/.
func (ps *projectScope) AuthorityTree(id string) (string, error) {
	p, err := ps.srv.projects.Get(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Workspace, knowledgebaseDir, "knowledgebase"), nil
}

// LocalTree returns the node-local path for the scope. On the authority this
// is the canonical tree itself; on a participant it is the node-local
// workspace root: <workspace_root>/<kind>/<id>/.horde/knowledgebase/.
func (ps *projectScope) LocalTree(id string) (string, error) {
	if ps.IsAuthority(id) {
		return ps.AuthorityTree(id)
	}
	root := ps.srv.cfg.KBSync.WorkspaceRoot
	if root == "" {
		// Empty DataDir/StateDir guard: if no data dir is configured, a
		// participant cannot materialize. Disabled behavior is served by the
		// sync-enabled check; this path should not be reached when disabled.
		if ps.srv.cfg.DataDir == "" {
			return "", errors.New("no workspace root or data dir configured for participant KB")
		}
		root = filepath.Join(ps.srv.cfg.DataDir, "workspaces")
	}
	return filepath.Join(root, kbScopeKindProject, id, knowledgebaseDir, "knowledgebase"), nil
}

// Participates reports whether this node syncs the project scope. In v1 every
// node that has sync enabled participates in every project. A participant
// learns projects from the authority over the existing forward path.
func (ps *projectScope) Participates(_ string) bool {
	return ps.srv.cfg.KBSync.Enabled
}

// Authorize checks the request principal's authority for the project scope.
// KB routes MUST NOT inherit the host's open-reads policy: document bytes
// cannot be redacted (KSP §9). Two paths:
//   - User principal: reads require project view authority (owner/admin/team
//     member); writes require project write authority.
//   - Node principal (cluster token): reads (convergence) are permitted with
//     no per-user identity — machine-initiated (KSP §9). A write must be
//     attributed: a participant API forward echoes the originating user, and a
//     stage-2 watcher push echoes its configured PushUser, via X-Horde-User.
//     The echoed id is re-derived against local config and subject to the
//     scope's write authority; a node write with no echoed user fails closed.
//
// When auth is disabled (no auth.users) authorization is a no-op, matching the
// rest of the API.
func (ps *projectScope) Authorize(r *http.Request, id string, w bool) error {
	// When auth is disabled, authorization is a no-op.
	if !ps.srv.AuthEnabled() {
		return nil
	}

	// Look up the project; an unknown project yields a 404 sentinel.
	if _, err := ps.srv.projects.Get(id); err != nil {
		return err
	}

	// Resolve the principal from the request context. This is set by
	// resolvePrincipal middleware (internal/api/principal.go). When this is
	// called from outside the API middleware chain (e.g. a test), the
	// zero-value anonymous principal is used — and denied.
	prin := resolvePrincipalFromRequest(r)

	switch prin.Kind {
	case KBPrincipalKindNode:
		// Reads (convergence) need no per-user identity (KSP §9).
		if !w {
			return nil
		}
		// A write is attributed via X-Horde-User: a participant API forward
		// carries the originating user; a stage-2 watcher push carries its
		// configured PushUser. Honoring the header is safe — a node principal
		// only resolves when the cluster token matched, so an external client
		// cannot forge it (mirrors the project-mutation forward model). The
		// id is re-derived against local config and subject to the scope's
		// write authority; no echoed user ⇒ no write identity ⇒ 403.
		uid := strings.TrimSpace(r.Header.Get(kbForwardedUserHeader))
		if uid == "" {
			return ErrKBForbidden
		}
		u, ok := ps.srv.userByID(uid)
		if !ok {
			return ErrKBForbidden
		}
		return authorizeKBWrite(ps.srv, id, u.ID, u.Admin)
	case KBPrincipalKindUser:
		if w {
			return authorizeKBWrite(ps.srv, id, prin.UserID, prin.Admin)
		}
		return authorizeKBRead(ps.srv, id, prin.UserID, prin.Admin)
	}

	return ErrKBForbidden
}

// Policy returns the authority's size and ignore policy for the project scope.
// The ignore policy is resolved the same way scanManifest resolves it: an
// empty config Ignore defaults to kbIgnoreGlobs(), so what the manifest
// publishes matches what the tree scan enforces (KSP §2.5).
func (ps *projectScope) Policy() KBScopePolicy {
	cfg := ps.srv.cfg.KBSync
	ignore := cfg.Ignore
	if len(ignore) == 0 {
		ignore = kbIgnoreGlobs()
	}
	return KBScopePolicy{
		MaxFileSize: cfg.MaxFileSize,
		Ignore:      ignore,
	}
}

// CachedManifest returns a cached or freshly-scanned manifest for the scope.
// On the authority it scans the canonical tree; on a participant it scans the
// local tree (serving local reads labeled X-KSP-Authority: participant).
func (ps *projectScope) CachedManifest(scope KBScopeRef) (*KBManifest, error) {
	root, err := ps.servingTree(scope.ID)
	if err != nil {
		return nil, err
	}
	authority := ps.srv.NodeID()
	if !ps.IsAuthority(scope.ID) {
		authority = "" // participant label handled by the handler
	}
	return ps.srv.kbManifestCache.cachedScan(root, ps.Policy(), authority, scope)
}

// ReadFile reads a file from the serving tree (authority canonical tree or
// participant local tree) for the given scope id. Returns bytes, digest,
// modtime, and error.
//
//nolint:gocritic // unnamedResult: results are clear from context
func (ps *projectScope) ReadFile(id, relPath string) ([]byte, string, time.Time, error) {
	root, err := ps.servingTree(id)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return kbReadFile(root, relPath)
}

// InvalidateCache forces the next manifest scan to re-read the tree for the
// given scope id.
func (ps *projectScope) InvalidateCache(id string) {
	root, err := ps.servingTree(id)
	if err != nil {
		return
	}
	ps.srv.kbManifestCache.invalidate(root)
}

// WriteFile writes a file to the canonical tree via temp+rename (KSP §4.3:
// the authority MUST write via temp-file + rename). Only meaningful on the
// authority; a participant forwards writes instead. Per-path serialization
// is the caller's responsibility (kbWriteMutex).
func (ps *projectScope) WriteFile(id, relPath string, data []byte) (string, error) {
	if !ps.IsAuthority(id) {
		return "", ErrKBForbidden
	}
	root, err := ps.AuthorityTree(id)
	if err != nil {
		return "", err
	}
	cleaned, err := ValidateKBPath(relPath)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, cleaned)
	if err := atomicWriteFile(full, data); err != nil {
		return "", err
	}
	digest, err := hashFile(full)
	if err != nil {
		return "", err
	}
	return digest, nil
}

// DeleteFile removes a file from the canonical tree (KSP §4.4). Returns
// ErrKBFileNotFound if the path doesn't exist. Only meaningful on the authority.
func (ps *projectScope) DeleteFile(id, relPath string) error {
	if !ps.IsAuthority(id) {
		return ErrKBForbidden
	}
	root, err := ps.AuthorityTree(id)
	if err != nil {
		return err
	}
	cleaned, err := ValidateKBPath(relPath)
	if err != nil {
		return err
	}
	full := filepath.Join(root, cleaned)
	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			return ErrKBFileNotFound
		}
		return err
	}
	return nil
}

// servingTree returns the tree path this node serves reads from for the given
// scope: the canonical tree on the authority, the local tree on a participant.
func (ps *projectScope) servingTree(id string) (string, error) {
	if ps.IsAuthority(id) {
		return ps.AuthorityTree(id)
	}
	return ps.LocalTree(id)
}

// KBPrincipal is a minimal principal shape the scope resolver reads from the
// request context. It mirrors api.principal without creating an import cycle:
// the API layer stashes this via a context value, and this package reads it
// through the exported key. The Kind string values must match: "user",
// "node", "anonymous".
type KBPrincipal struct {
	Kind   KBPrincipalKind
	UserID string
	Admin  bool
}

// KBPrincipalKind mirrors api.principalKind without the import.
type KBPrincipalKind string

const (
	KBPrincipalKindUser      KBPrincipalKind = "user"
	KBPrincipalKindNode      KBPrincipalKind = "node"
	KBPrincipalKindAnonymous KBPrincipalKind = "anonymous"
)

// KBPrincipalCtxKey is the context key for the stashed KB principal. The API
// layer sets this via resolvePrincipal so KB scope resolvers can authorize
// without importing internal/api.
type KBPrincipalCtxKey struct{}

// resolvePrincipalFromRequest reads the stashed principal from the request
// context (set by the API layer's resolvePrincipal middleware). When no
// principal is stashed (e.g. a test calling the handler directly), it returns
// an anonymous principal.
func resolvePrincipalFromRequest(r *http.Request) KBPrincipal {
	v := r.Context().Value(KBPrincipalCtxKey{})
	if v == nil {
		return KBPrincipal{Kind: KBPrincipalKindAnonymous}
	}
	p, ok := v.(KBPrincipal)
	if !ok {
		return KBPrincipal{Kind: KBPrincipalKindAnonymous}
	}
	return p
}

// ErrKBForbidden is the sentinel for KB authorization denial (mapped to 403).
var ErrKBForbidden = errors.New("kb: forbidden")

// ErrKBFileNotFound is the sentinel for a KB file not found in the tree
// (mapped to 404).
var ErrKBFileNotFound = errors.New("kb: file not found")

// authorizeKBRead checks view authority for a user: admin, owner, or team
// member (levelView in the project authz model).
func authorizeKBRead(srv *Server, projectID, userID string, admin bool) error {
	p, err := srv.projects.Get(projectID)
	if err != nil {
		return err
	}
	if admin || userID == p.Owner {
		return nil
	}
	for _, u := range p.Team.Users {
		if u.UserID == userID {
			return nil
		}
	}
	return ErrKBForbidden
}

// authorizeKBWrite checks write authority for a user. The host's project write
// authority is levelOwn: only the owner (or an admin) may write KB files.
func authorizeKBWrite(srv *Server, projectID, userID string, admin bool) error {
	p, err := srv.projects.Get(projectID)
	if err != nil {
		return err
	}
	if admin || userID == p.Owner {
		return nil
	}
	return ErrKBForbidden
}

// KBResolveScope looks up a registered scope resolver by kind. Returns nil
// for an unregistered kind (the handler maps this to 404 per KSP §2.1).
func (s *Server) KBResolveScope(kind string) ScopeResolver {
	if kind == kbScopeKindProject && s.kbScopes != nil {
		return s.kbScopes[kbScopeKindProject]
	}
	return nil
}

// kbIgnoreGlobs returns the default ignore globs applied to KB scans when no
// explicit config is set. Covers editor swap files and git metadata.
func kbIgnoreGlobs() []string {
	return []string{"*.tmp", "*.swp", "*~", ".git/**"}
}

// kbMatchesIgnore reports whether path matches any of the ignore globs. The
// globs are KB-root-relative (e.g. ".git/**", "*.tmp"). Matching uses
// filepath.Match for simple patterns; a pattern containing "/" is matched
// against the full relative path with doublestar-style semantics (path
// segments separated by "/").
func kbMatchesIgnore(path string, globs []string) bool {
	cleanPath := filepath.Clean(path)
	for _, g := range globs {
		if kbMatchGlob(cleanPath, g) {
			return true
		}
	}
	return false
}

// kbMatchGlob matches a path against a glob pattern. It handles "**" as
// matching zero or more path segments, and "*" within a single segment.
func kbMatchGlob(path, pattern string) bool {
	// Fast path: no "**" — use filepath.Match directly.
	if !strings.Contains(pattern, "**") {
		matched, _ := filepath.Match(pattern, path)
		return matched
	}
	// "**" matching: split both into segments and match recursively.
	pSegs := strings.Split(path, "/")
	gSegs := strings.Split(pattern, "/")
	return kbMatchSegments(pSegs, gSegs)
}

// kbMatchSegments matches path segments against pattern segments, where "**"
// matches zero or more segments.
func kbMatchSegments(path, pattern []string) bool {
	if len(pattern) == 0 {
		return len(path) == 0
	}
	if pattern[0] == "**" {
		// "**" matches zero or more segments: try consuming 0..len(path).
		for i := 0; i <= len(path); i++ {
			if kbMatchSegments(path[i:], pattern[1:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	matched, _ := filepath.Match(pattern[0], path[0])
	if !matched {
		return false
	}
	return kbMatchSegments(path[1:], pattern[1:])
}

// ErrKBInvalidPath is the sentinel for a KB path that fails validation
// (mapped to 400).
var ErrKBInvalidPath = errors.New("kb: invalid path")

// ValidateKBPath checks that a path is normalized and does not escape the KB
// root (KSP §2.2). Returns the cleaned path, or an error for any ".."
// path component, absolute path, or backslash separator.
func ValidateKBPath(path string) (string, error) {
	cleaned := filepath.Clean("/" + path)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if strings.HasPrefix(path, "/") {
		return "", ErrKBInvalidPath
	}
	// Reject ".." as a path component (not substring) so legitimate filenames
	// like "notes..md" or "v1..v2.md" are allowed.
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == ".." {
			return "", ErrKBInvalidPath
		}
	}
	// Reject backslash-separated paths on all platforms (KSP uses "/" only).
	if strings.Contains(path, "\\") {
		return "", ErrKBInvalidPath
	}
	return cleaned, nil
}

// kbDigestPrefix is the prefix for content digests in entries.
const kbDigestPrefix = "sha256:"

// isKBFile returns true if the path is a regular file (not a directory, not a
// symlink, not a device).
func isKBFile(info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
