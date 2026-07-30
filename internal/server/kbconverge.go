package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/sirupsen/logrus"
)

// kbClientTimeout is the HTTP timeout for KB client reads (file bytes, not
// JSON, so a longer timeout than the leader client).
const kbClientTimeout = 10 * time.Second

// kbClient is the convergence-side HTTP client for fetching the authority's
// manifest and file bytes (KSP §4.1, §4.2). It uses the cluster token for
// node→node auth (KSP §9: the convergence-read path does not need a per-user
// identity). The client is separate from leaderClient because KB reads have
// different timeout requirements (file bytes, not JSON) and different header
// handling (ETag, If-None-Match).
type kbClient struct {
	token string // cluster auth token
	hc    *http.Client
}

func newKBClient(token string) *kbClient {
	return &kbClient{
		token: token,
		hc:    &http.Client{Timeout: kbClientTimeout},
	}
}

// fetchManifest gets the authority's manifest for a scope, with optional
// If-None-Match. Returns the manifest, the HTTP status (200 or 304), and an
// error. A 304 means the manifest hasn't changed since the given digest.
func (c *kbClient) fetchManifest(ctx context.Context, addr string, scope KBScopeRef, ifNoneMatch string) (*KBManifest, int, error) {
	u := fmt.Sprintf("http://%s/api/v1/kb/%s/%s/manifest", addr, scope.Kind, scope.ID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	SetClusterAuth(req.Header, c.token)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return nil, http.StatusNotModified, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, resp.StatusCode, fmt.Errorf("fetch manifest: status %d: %s", resp.StatusCode, body)
	}

	var manifest KBManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode manifest: %w", err)
	}

	// Verify the manifest's scope matches the one requested (KSP §10:
	// scope isolation — a manifest from one scope must not be applied to
	// another).
	if manifest.Scope.Kind != scope.Kind || manifest.Scope.ID != scope.ID {
		return nil, resp.StatusCode, fmt.Errorf("manifest scope mismatch: requested %s/%s, got %s/%s",
			scope.Kind, scope.ID, manifest.Scope.Kind, manifest.Scope.ID)
	}

	return &manifest, resp.StatusCode, nil
}

// fetchFile gets a file's bytes from the authority's canonical tree (KSP §4.2).
func (c *kbClient) fetchFile(ctx context.Context, addr string, scope KBScopeRef, relPath string) ([]byte, error) {
	q := url.Values{}
	q.Set("path", relPath)
	u := fmt.Sprintf("http://%s/api/v1/kb/%s/%s/file?%s", addr, scope.Kind, scope.ID, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	SetClusterAuth(req.Header, c.token)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch file %s: status %d: %s", relPath, resp.StatusCode, body)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read file body: %w", err)
	}

	return data, nil
}

// kbConverger runs the periodic convergence loop on a participant node (KSP
// §5.3). It polls the authority's manifest, classifies each path via the
// three-way comparison (§5.1), and pulls/deletes/preserves as needed. A
// non-pushing participant does not push (§5.2): local edits are preserved to
// the conflict area and the authority's version is pulled.
type kbConverger struct {
	srv      *Server
	client   *kbClient
	syncMgr  *kbSyncStoreManager
	conflict *kbConflictArea
}

// newKBConverger creates a convergence loop bound to the server. Returns nil
// when sync is disabled or on the authority (no convergence needed). The
// leader address is resolved per-poll, so a DNS-discovered leader that moves
// is picked up without a restart.
func newKBConverger(srv *Server) *kbConverger {
	if srv.leader == nil {
		return nil
	}
	return &kbConverger{
		srv:      srv,
		client:   newKBClient(srv.cfg.AuthToken),
		syncMgr:  srv.kbSyncMgr,
		conflict: srv.kbConflict,
	}
}

// leaderAddr resolves the current leader address per-poll, supporting DNS
// discovery and leader moves.
func (c *kbConverger) leaderAddr() string {
	if c.srv.leader == nil {
		return ""
	}
	return c.srv.leader.leaderAddr()
}

// run is the periodic convergence loop (KSP §5.3). It polls the authority's
// manifest at the configured interval, classifies each path, and applies the
// convergence actions. Exits on ctx cancel.
func (c *kbConverger) run(ctx context.Context) {
	interval := c.srv.cfg.KBSync.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second //nolint:mnd // 30s default poll interval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately on startup, then on each tick.
	c.convergeAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.convergeAll(ctx)
		}
	}
}

// convergeAll runs one convergence pass over all participating project scopes.
func (c *kbConverger) convergeAll(ctx context.Context) {
	if !c.srv.KBSyncEnabled() {
		return
	}

	// In v1 every node that has sync enabled participates in every project
	// (KSP §3.2). On the authority there's nothing to converge — it IS the
	// canonical state.
	if c.srv.isMaster() {
		return
	}

	// Resolve the leader address per-poll. Under DNS discovery the address
	// may be empty at startup and resolve later; skip this tick if still
	// unresolved.
	addr := c.leaderAddr()
	if addr == "" {
		return
	}

	resolver := c.srv.KBResolveScope(kbScopeKindProject)
	if resolver == nil {
		return
	}

	projects := c.srv.ListProjects("")
	for i := range projects {
		p := &projects[i]
		scope := KBScopeRef{Kind: kbScopeKindProject, ID: p.ID}
		if err := c.convergeScope(ctx, resolver, scope, addr); err != nil {
			logrus.WithError(err).WithField(logKeyProject, p.ID).Warn("kb convergence: scope failed")
		}
	}
}

// convergeScope converges one project scope against the authority.
func (c *kbConverger) convergeScope(ctx context.Context, resolver ScopeResolver, scope KBScopeRef, addr string) error {
	syncStore := c.syncMgr.Get(scope.Kind, scope.ID)

	// Fetch the authority's manifest with If-None-Match for the last-seen
	// digest (KSP §5.3: nodes poll using If-None-Match, making the steady-
	// state poll nearly free). The empty-path record stores the manifest digest.
	lastDigest := syncStore.Get("")
	manifest, status, err := c.client.fetchManifest(ctx, addr, scope, lastDigest)
	if err != nil {
		return fmt.Errorf("fetch manifest: %w", err)
	}
	if status == http.StatusNotModified {
		return nil // nothing changed since last poll
	}

	// Persist the new manifest digest for the next poll's If-None-Match.
	syncStore.Set("", manifest.ManifestDigest)

	// Build the authority's path→digest map from the manifest entries.
	authorityEntries := make(map[string]string, len(manifest.Files))
	for _, e := range manifest.Files {
		authorityEntries[e.Path] = e.Digest
	}

	// Scan the local tree using the authority's published policy (KSP §5.4:
	// a node MUST NOT apply its own ignore globs or size caps to reject
	// authority content).
	localTree, err := resolver.LocalTree(scope.ID)
	if err != nil {
		return fmt.Errorf("resolve local tree: %w", err)
	}
	localDigests, err := scanLocalDigests(localTree, manifest.Policy)
	if err != nil {
		return fmt.Errorf("scan local tree: %w", err)
	}

	// Collect all paths from both the authority manifest and local disk +
	// sync records. Each path is classified independently.
	synced := syncStore.All()
	allPaths := make(map[string]struct{})
	for p := range authorityEntries {
		allPaths[p] = struct{}{}
	}
	for p := range localDigests {
		allPaths[p] = struct{}{}
	}
	for p := range synced {
		if p == "" {
			continue // skip the manifest-digest record
		}
		allPaths[p] = struct{}{}
	}

	for path := range allPaths {
		a := authorityEntries[path] // "" if absent
		s := synced[path]           // "" if never synced
		d := localDigests[path]     // "" if not on disk

		result := classifyStage1(a, s, d)
		if err := c.applyConvergence(ctx, scope, syncStore, path, result, localTree, addr); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				logKeyProject: scope.ID,
				logKeyPath:    path,
				"action":      result.Action,
			}).Warn("kb convergence: apply failed")
		}
	}

	return nil
}

// applyConvergence executes the convergence action for one path.
func (c *kbConverger) applyConvergence(ctx context.Context, scope KBScopeRef, syncStore *kbSyncRecordStore, path string, result kbClassifyResult, localTree, addr string) error {
	switch result.Action {
	case kbActNone:
		return nil

	case kbActPull, kbActPullNew:
		// Re-validate the authority-supplied path before writing (KSP §10:
		// a node MUST re-validate a path from the manifest before writing,
		// never trusting the authority blindly).
		if _, err := ValidateKBPath(path); err != nil {
			logrus.WithError(err).WithField(logKeyPath, path).
				Warn("kb convergence: rejecting invalid authority path")
			return nil
		}
		data, err := c.client.fetchFile(ctx, addr, scope, path)
		if err != nil {
			return fmt.Errorf("pull file: %w", err)
		}
		// Verify the pulled bytes against the manifest's digest by hashing
		// the actual data (KSP §10: a node SHOULD verify pulled bytes and
		// discard a mismatch — not just compare ETag headers).
		if result.AuthorityDigest != "" {
			dataDigest := sha256Hex(data)
			if dataDigest != result.AuthorityDigest {
				logrus.WithFields(logrus.Fields{
					logKeyPath: path,
					"expected": result.AuthorityDigest,
					"got":      dataDigest,
				}).Warn("kb convergence: pulled file digest mismatch, discarding")
				return nil // discard, do not write or set sync record
			}
		}
		full := filepath.Join(localTree, path)
		if err := atomicWriteFile(full, data); err != nil {
			return fmt.Errorf("write pulled file: %w", err)
		}
		syncStore.Set(path, result.AuthorityDigest)
		return nil

	case kbActDeleteLocal:
		full := filepath.Join(localTree, path)
		if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete local file: %w", err)
		}
		syncStore.Delete(path)
		return nil

	case kbActDropRecord:
		// Stale sync record with no file on disk or upstream — drop it.
		syncStore.Delete(path)
		return nil

	case kbActConflict:
		return c.applyConflict(ctx, scope, syncStore, path, result, localTree, addr)

	default:
		return nil
	}
}

// applyConflict handles a conflict for a non-pushing node: preserve the dirty
// local file to the conflict area, then converge to canonical (KSP §5.2).
func (c *kbConverger) applyConflict(ctx context.Context, scope KBScopeRef, syncStore *kbSyncRecordStore, path string, result kbClassifyResult, localTree, addr string) error {
	if result.DiskDigest != "" {
		full := filepath.Join(localTree, path)
		conflictPath, err := c.conflict.Preserve(scope, path, full)
		if err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				logKeyProject: scope.ID,
				logKeyPath:    path,
			}).Warn("kb convergence: preserve conflict failed")
		} else {
			logrus.WithFields(logrus.Fields{
				logKeyProject: scope.ID,
				logKeyPath:    path,
				"conflict":    conflictPath,
			}).Warn("kb convergence: local edit preserved to conflict area")
		}
	}
	// Now converge to canonical: if the authority has the file, pull it;
	// if the authority deleted it, delete locally.
	if result.AuthorityDigest != "" {
		if _, err := ValidateKBPath(path); err != nil {
			logrus.WithError(err).WithField(logKeyPath, path).
				Warn("kb convergence: rejecting invalid authority path")
			return nil
		}
		data, err := c.client.fetchFile(ctx, addr, scope, path)
		if err != nil {
			return fmt.Errorf("pull file after conflict: %w", err)
		}
		// Verify the pulled bytes (KSP §10).
		dataDigest := sha256Hex(data)
		if dataDigest != result.AuthorityDigest {
			logrus.WithFields(logrus.Fields{
				logKeyPath: path,
				"expected": result.AuthorityDigest,
				"got":      dataDigest,
			}).Warn("kb convergence: post-conflict pull digest mismatch, discarding")
			return nil
		}
		full := filepath.Join(localTree, path)
		if err := atomicWriteFile(full, data); err != nil {
			return fmt.Errorf("write file after conflict: %w", err)
		}
		syncStore.Set(path, result.AuthorityDigest)
	} else {
		full := filepath.Join(localTree, path)
		_ = os.Remove(full)
		syncStore.Delete(path)
	}
	return nil
}

// scanLocalDigests walks a local tree and returns path→digest for each regular
// file, using the authority's published policy for ignore globs and size cap
// (KSP §5.4: a node MUST NOT apply its own ignore globs or size caps to reject
// authority content).
func scanLocalDigests(root string, policy KBScopePolicy) (map[string]string, error) {
	digests := make(map[string]string)
	ignore := policy.Ignore
	if len(ignore) == 0 {
		ignore = kbIgnoreGlobs()
	}
	maxSize := policy.MaxFileSize
	if maxSize == 0 {
		maxSize = DefaultKBMaxFileSize
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // walk continues past the errored entry
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil //nolint:nilerr // walk continues past the errored entry
		}
		rel = filepath.ToSlash(rel)
		if kbMatchesIgnore(rel, ignore) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // walk continues past the errored entry
		}
		if info.Size() > maxSize {
			return nil
		}
		digest, err := hashFile(path)
		if err != nil {
			return nil //nolint:nilerr // walk continues past the errored entry
		}
		digests[rel] = digest
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return digests, nil
}

// atomicWriteFile writes data to path via a temp file + rename, so a reader
// never observes a torn file (KSP §4.3: the authority writes via temp+rename;
// a participant applies pulled content the same way).
func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), kbDirPerm); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, kbFilePerm); err != nil { //#nosec G304,G306 // path is KB-internal, standard KB file permissions
		return err
	}
	return os.Rename(tmp, path)
}

// sha256Hex returns the "sha256:<hex>" digest of data, for verifying pulled
// bytes against the manifest's expected digest (KSP §10).
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return kbDigestPrefix + hex.EncodeToString(h[:])
}
