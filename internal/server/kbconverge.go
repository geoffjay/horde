package server

import (
	"bytes"
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
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

// kbClientTimeout is the HTTP timeout for KB client reads (file bytes, not
// JSON, so a longer timeout than the leader client).
const kbClientTimeout = 10 * time.Second

// kbLogFieldGot is the logrus field name for a computed/observed digest that
// didn't match the expected one (digest mismatch warnings).
const kbLogFieldGot = "got"

// kbLogFieldExpected is the logrus field name for the expected digest in a
// digest mismatch warning.
const kbLogFieldExpected = "expected"

// kbClient is the convergence-side HTTP client for fetching the authority's
// manifest and file bytes (KSP §4.1, §4.2). It uses the cluster token for
// node→node auth (KSP §9: the convergence-read path does not need a per-user
// identity). The client is separate from leaderClient because KB reads have
// different timeout requirements (file bytes, not JSON) and different header
// handling (ETag, If-None-Match).
type kbClient struct {
	token    string // cluster auth token
	pushUser string // X-Horde-User echoed on push writes (KSP §9 attribution)
	hc       *http.Client
}

func newKBClient(token, pushUser string) *kbClient {
	return &kbClient{
		token:    token,
		pushUser: pushUser,
		hc:       &http.Client{Timeout: kbClientTimeout},
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

// kbLeaderProject is the minimal project shape the converger needs from the
// leader's project list: the id and lifecycle state. It mirrors the fields of
// the API's projectDTO that convergence cares about.
type kbLeaderProject struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// listProjects fetches the project list from the leader (the authority is the
// source of truth). A participant's own project store is empty — project API
// requests forward to the coordinator (projectForwardMiddleware) and never populate
// the local store — so the converger MUST learn participating projects from
// the leader rather than reading its local store (KSP §3.2: participants learn
// projects from the authority).
func (c *kbClient) listProjects(ctx context.Context, addr string) ([]kbLeaderProject, error) {
	u := fmt.Sprintf("http://%s/api/v1/projects", addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	SetClusterAuth(req.Header, c.token)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list projects: status %d: %s", resp.StatusCode, body)
	}

	var projects []kbLeaderProject
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, fmt.Errorf("decode projects: %w", err)
	}
	return projects, nil
}

// putFile pushes a local file to the authority via CAS (KSP §4.3, stage 2
// push row). If-Match is the synced_digest the edit was based on; If-None-Match:
// * creates a new file. Returns the new digest from the ETag header on
// success, the HTTP status, and an error. A 412 means the CAS precondition
// failed — the authority's current digest is in the ETag response header.
//
//nolint:gocritic // unnamedResult: results are clear from context (etag, status, error)
func (c *kbClient) putFile(ctx context.Context, addr string, scope KBScopeRef, relPath string, data []byte, ifMatch, ifNoneMatch string) (string, int, error) {
	q := url.Values{}
	q.Set("path", relPath)
	u := fmt.Sprintf("http://%s/api/v1/kb/%s/%s/file?%s", addr, scope.Kind, scope.ID, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return "", 0, err
	}
	SetClusterAuth(req.Header, c.token)
	if c.pushUser != "" {
		req.Header.Set(kbForwardedUserHeader, c.pushUser)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("push file: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	etag := strings.Trim(resp.Header.Get("ETag"), "\"")
	return etag, resp.StatusCode, nil
}

// deleteFile pushes a delete to the authority via CAS (KSP §4.4, stage 2
// push-delete row). If-Match is the synced_digest the delete was based on.
// Returns the HTTP status and error. A 412 means the CAS precondition failed;
// 404 means the file is already gone upstream.
func (c *kbClient) deleteFile(ctx context.Context, addr string, scope KBScopeRef, relPath, ifMatch string) (int, error) {
	q := url.Values{}
	q.Set("path", relPath)
	u := fmt.Sprintf("http://%s/api/v1/kb/%s/%s/file?%s", addr, scope.Kind, scope.ID, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, http.NoBody)
	if err != nil {
		return 0, err
	}
	SetClusterAuth(req.Header, c.token)
	if c.pushUser != "" {
		req.Header.Set(kbForwardedUserHeader, c.pushUser)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("push delete: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode, nil
}

// kbConverger runs the periodic convergence loop on a participant node (KSP
// §5.3). It polls the authority's manifest, classifies each path via the
// three-way comparison (§5.1), and pulls/deletes/preserves as needed. When
// pushing (WatchLocal enabled, stage 2), it also pushes local edits to the
// authority using synced_digest as the If-Match base; a 412 conflict
// preserves the local content to the conflict area before converging (§6).
// A non-pushing participant does not push (§5.2): local edits are preserved
// to the conflict area and the authority's version is pulled.
type kbConverger struct {
	srv      *Server
	client   *kbClient
	syncMgr  *kbSyncStoreManager
	conflict *kbConflictArea
	// pushing is true when WatchLocal is enabled (stage 2). A pushing node
	// executes the push rows of KSP §5.1; a non-pushing node maps them to
	// conflicts (§5.2).
	pushing bool
	// earlyPoll is signaled by the participant watcher to trigger an
	// immediate convergence pass (KSP §5.3: a change signal triggers an
	// early poll; strictly a latency optimization — correctness does not
	// depend on it). Non-blocking: the converger coalesces rapid signals.
	earlyPoll chan struct{}
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
		srv:       srv,
		client:    newKBClient(srv.cfg.AuthToken, srv.cfg.KBSync.PushUser),
		syncMgr:   srv.kbSyncMgr,
		conflict:  srv.kbConflict,
		pushing:   srv.cfg.KBSync.WatchLocal,
		earlyPoll: make(chan struct{}, 1),
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

// triggerEarlyPoll signals the convergence loop to run an immediate pass
// (KSP §5.3). Called by the participant watcher when a local file change is
// detected. Non-blocking: if a pass is already pending, the signal is
// coalesced (buffered channel of 1).
func (c *kbConverger) triggerEarlyPoll() {
	select {
	case c.earlyPoll <- struct{}{}:
	default:
	}
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
		case <-c.earlyPoll:
			// The participant watcher signaled a local tree change —
			// run an immediate convergence pass to push the edit (KSP
			// §5.3: a change signal triggers an early poll). Drain any
			// extra signals (coalesce rapid events).
			select {
			case <-c.earlyPoll:
			default:
			}
			c.convergeAll(ctx)
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
	if c.srv.isCoordinator() {
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

	// Collect participating project IDs. A real worker's local store is empty
	// (project API requests forward to the coordinator), so the leader's list is
	// the source of truth (KSP §3.2). Union with the local store so the
	// in-process/raft cases (where the store is populated) also work.
	ids := make(map[string]struct{})
	local := c.srv.ListProjects(string(ProjectActive))
	for i := range local {
		ids[local[i].ID] = struct{}{}
	}
	if leaderProjects, err := c.client.listProjects(ctx, addr); err != nil {
		logrus.WithError(err).Debug("kb convergence: list leader projects failed; using local store only")
	} else {
		for _, p := range leaderProjects {
			if p.State == "" || p.State == string(ProjectActive) {
				ids[p.ID] = struct{}{}
			}
		}
	}

	for id := range ids {
		scope := KBScopeRef{Kind: kbScopeKindProject, ID: id}
		if err := c.convergeScope(ctx, resolver, scope, addr); err != nil {
			logrus.WithError(err).WithField(logKeyProject, id).Warn("kb convergence: scope failed")
		}
	}
}

// convergeScope converges one project scope against the authority.
//
//nolint:gocyclo,funlen // KSP §5.1 convergence — the 304-reuse path adds branches
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
		// The authority's manifest hasn't changed. A non-pushing node has
		// nothing to do (no remote changes, local edits are preserved not
		// pushed — KSP §5.2). A pushing node (stage 2) still needs to
		// classify local edits against the last-known manifest to push
		// them. Use the cached manifest entries from the sync records.
		if !c.pushing {
			return nil
		}
		// Reconstruct the authority's entries from the last sync records:
		// every path with a synced_digest was in the last manifest. This is
		// an approximation (the manifest may have had entries the node
		// never synced), but it is sufficient for push classification — a
		// path with S set and D ≠ S will classify as push regardless of A.
		manifest = nil // will build from sync records below
	} else {
		// Persist the new manifest digest for the next poll's If-None-Match.
		syncStore.Set("", manifest.ManifestDigest)
	}

	// Build the authority's path→digest map from the manifest entries, or
	// from sync records when the manifest was a 304 (pushing node reuse).
	authorityEntries := make(map[string]string)
	if manifest != nil {
		for _, e := range manifest.Files {
			authorityEntries[e.Path] = e.Digest
		}
	} else {
		// 304 on a pushing node: reconstruct from sync records. A path with
		// a synced_digest was in the last manifest at that digest. This is
		// a safe lower bound for classification: A=S for known paths, and
		// any D ≠ S triggers a push.
		for p, d := range syncStore.All() {
			if p == "" {
				continue // skip the manifest-digest record
			}
			authorityEntries[p] = d
		}
	}

	// Scan the local tree using the authority's published policy (KSP §5.4:
	// a node MUST NOT apply its own ignore globs or size caps to reject
	// authority content).
	localTree, err := resolver.LocalTree(scope.ID)
	if err != nil {
		return fmt.Errorf("resolve local tree: %w", err)
	}
	var policy KBScopePolicy
	if manifest != nil {
		policy = manifest.Policy
	}
	localDigests, err := scanLocalDigests(localTree, policy)
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
		if c.pushing {
			result = classifyStage2(a, s, d)
		}
		if err := c.applyConvergence(ctx, scope, syncStore, path, result, localTree, addr); err != nil {
			logrus.WithError(err).WithFields(logrus.Fields{
				logKeyProject: scope.ID,
				logKeyPath:    path,
				"action":      result.Action,
			}).Warn("kb convergence: apply failed")
		}
	}

	// Ensure the participant's local tree is being watched now that it
	// may have been created by the first pull (stage 2). Idempotent.
	c.srv.kbEnsureWatched(scope.ID)
	return nil
}

// applyConvergence executes the convergence action for one path.
//
//nolint:gocyclo // KSP §5.1 action dispatch — one case per action
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
					logKeyPath:         path,
					kbLogFieldExpected: result.AuthorityDigest,
					kbLogFieldGot:      dataDigest,
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

	case kbActPush:
		// Local edit only, clean upstream — push If-Match: S (KSP §5.1,
		// stage 2). On 200, set S to the new digest. On 412, the authority
		// moved: preserve local content to the conflict area, then converge
		// to canonical (§6).
		return c.applyPush(ctx, scope, syncStore, path, result, localTree, addr)

	case kbActPushNew:
		// New local file — push If-None-Match: * (KSP §5.1, stage 2).
		return c.applyPushNew(ctx, scope, syncStore, path, result, localTree, addr)

	case kbActPushDelete:
		// Deleted locally, exists upstream — push DELETE If-Match: S
		// (KSP §5.1, stage 2). On 200, drop the sync record. On 412, the
		// authority moved: pull the authority's current version.
		return c.applyPushDelete(ctx, scope, syncStore, path, result, localTree, addr)

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
				logKeyPath:         path,
				kbLogFieldExpected: result.AuthorityDigest,
				kbLogFieldGot:      dataDigest,
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

// applyPush pushes a locally-edited file to the authority via CAS (KSP §5.1
// push row, stage 2). Uses synced_digest as the If-Match base. On 200, sets
// S to the new digest. On 412, the authority moved: preserve the local content
// to the conflict area, then converge to canonical (§6). On a transient
// error, the local edit stays on disk and will be retried on the next poll.
func (c *kbConverger) applyPush(ctx context.Context, scope KBScopeRef, syncStore *kbSyncRecordStore, path string, result kbClassifyResult, localTree, addr string) error {
	full := filepath.Join(localTree, path)
	data, err := os.ReadFile(full) //#nosec G304 // localPath is within the node's KB tree
	if err != nil {
		return fmt.Errorf("read local file for push: %w", err)
	}
	newDigest, status, err := c.client.putFile(ctx, addr, scope, path, data, result.SyncedDigest, "")
	if err != nil {
		return fmt.Errorf("push file: %w", err)
	}
	switch status {
	case http.StatusOK:
		syncStore.Set(path, newDigest)
		return nil
	case http.StatusPreconditionFailed:
		// 412: another write landed first. Preserve local content to the
		// conflict area, then converge to canonical (KSP §6).
		return c.applyConflict(ctx, scope, syncStore, path, result, localTree, addr)
	default:
		return fmt.Errorf("push file: unexpected status %d", status)
	}
}

// applyPushNew pushes a new local file to the authority via CAS (KSP §5.1
// push-new row, stage 2). Uses If-None-Match: * (create only). On 200 or 201,
// sets S to the new digest. On 409 (path exists upstream), converge to
// canonical (pull the authority's version). On 412, treat as a conflict.
func (c *kbConverger) applyPushNew(ctx context.Context, scope KBScopeRef, syncStore *kbSyncRecordStore, path string, result kbClassifyResult, localTree, addr string) error {
	full := filepath.Join(localTree, path)
	data, err := os.ReadFile(full) //#nosec G304 // localPath is within the node's KB tree
	if err != nil {
		return fmt.Errorf("read local file for push: %w", err)
	}
	newDigest, status, err := c.client.putFile(ctx, addr, scope, path, data, "", "*")
	if err != nil {
		return fmt.Errorf("push new file: %w", err)
	}
	switch status {
	case http.StatusOK:
		syncStore.Set(path, newDigest)
		return nil
	case http.StatusConflict:
		// Path exists upstream — converge to canonical (pull it).
		return c.applyConflict(ctx, scope, syncStore, path, result, localTree, addr)
	case http.StatusPreconditionFailed:
		return c.applyConflict(ctx, scope, syncStore, path, result, localTree, addr)
	default:
		return fmt.Errorf("push new file: unexpected status %d", status)
	}
}

// applyPushDelete pushes a local deletion to the authority via CAS (KSP §5.1
// push-delete row, stage 2). Uses If-Match: synced_digest. On 200, drops the
// sync record. On 412, the authority moved: re-materialize the authority's
// current version (pull it back). On 404, the file is already gone upstream —
// drop the sync record.
func (c *kbConverger) applyPushDelete(ctx context.Context, scope KBScopeRef, syncStore *kbSyncRecordStore, path string, result kbClassifyResult, localTree, addr string) error {
	status, err := c.client.deleteFile(ctx, addr, scope, path, result.SyncedDigest)
	if err != nil {
		return fmt.Errorf("push delete: %w", err)
	}
	switch status {
	case http.StatusOK:
		syncStore.Delete(path)
		return nil
	case http.StatusNotFound:
		// Already gone upstream — drop the stale record.
		syncStore.Delete(path)
		return nil
	case http.StatusPreconditionFailed:
		// 412: the authority's version changed. Re-materialize from the
		// authority (pull the current version back).
		if _, err := ValidateKBPath(path); err != nil {
			logrus.WithError(err).WithField(logKeyPath, path).
				Warn("kb convergence: rejecting invalid authority path")
			return nil
		}
		data, err := c.client.fetchFile(ctx, addr, scope, path)
		if err != nil {
			return fmt.Errorf("pull file after push-delete 412: %w", err)
		}
		if result.AuthorityDigest != "" {
			dataDigest := sha256Hex(data)
			if dataDigest != result.AuthorityDigest {
				logrus.WithFields(logrus.Fields{
					logKeyPath:         path,
					kbLogFieldExpected: result.AuthorityDigest,
					kbLogFieldGot:      dataDigest,
				}).Warn("kb convergence: post-push-delete pull digest mismatch, discarding")
				return nil
			}
		}
		full := filepath.Join(localTree, path)
		if err := atomicWriteFile(full, data); err != nil {
			return fmt.Errorf("write file after push-delete: %w", err)
		}
		syncStore.Set(path, result.AuthorityDigest)
		return nil
	default:
		return fmt.Errorf("push delete: unexpected status %d", status)
	}
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
