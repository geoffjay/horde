package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/geoffjay/horde/internal/server"
)

// xKSPAuthorityHeader labels whether a response is from the authority or a
// participant (KSP §4.1, §4.2).
const xKSPAuthorityHeader = "X-KSP-Authority"

// xKSPEnabledHeader labels whether KSP sync is enabled for a scope (KSP §8).
const xKSPEnabledHeader = "X-KSP-Enabled"

// errKBScopeNotFound is the error message for an unknown scope.
const errKBScopeNotFound = "scope not found"

// Repeated error messages (extracted for goconst).
const (
	errKBDisabled     = "knowledgebase sync is disabled"
	errKBMissingPath  = "missing path query parameter"
	errKBFileNotFound = "file not found in knowledgebase"
	errKBInvalidPath  = "invalid path"
	errKBPrecondition = "precondition failed"
)

// kspAuthorityLabel returns "authority" or "participant" for the header.
func kspAuthorityLabel(resolver server.ScopeResolver, id string) string {
	if resolver.IsAuthority(id) {
		return "authority"
	}
	return "participant"
}

// getKBManifest handles GET /api/v1/kb/{kind}/{id}/manifest (KSP §4.1).
// Returns the manifest of whichever node serves it. Supports
// If-None-Match: <manifest_digest> → 304 Not Modified, making the steady-state
// poll nearly free. The X-KSP-Authority header states whether it is canonical.
//
// When sync is disabled (the default) the route returns 501 with
// X-KSP-Enabled: false, distinguishing "sync disabled" from "empty knowledgebase"
// (KSP §8). An unregistered kind returns 404 (KSP §2.1).
func getKBManifest(srv kbView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := chi.URLParam(r, "kind")
		id := chi.URLParam(r, "id")

		// Disabled ⇒ 501 (KSP §8).
		if !srv.KBSyncEnabled() {
			w.Header().Set(xKSPEnabledHeader, "false")
			writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errKBDisabled})
			return
		}

		// Unregistered kind ⇒ 404 (KSP §2.1).
		resolver := srv.KBResolveScope(kind)
		if resolver == nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown knowledgebase scope kind: " + kind})
			return
		}

		// Validate the scope id (e.g. project exists).
		if err := resolver.Validate(id); err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBScopeNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}

		// Authorization (KSP §9). KB routes MUST NOT inherit open-reads.
		if err := resolver.Authorize(r, id, false); err != nil {
			writeKBAuthzError(w, err)
			return
		}

		// Build the manifest.
		scope := server.KBScopeRef{Kind: kind, ID: id}
		manifest, err := resolver.CachedManifest(scope)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "scan manifest: " + err.Error()})
			return
		}

		// If-None-Match → 304 (KSP §4.1).
		etag := "\"" + manifest.ManifestDigest + "\""
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			if matchesETag(inm, manifest.ManifestDigest) {
				w.Header().Set("ETag", etag)
				w.Header().Set(xKSPAuthorityHeader, kspAuthorityLabel(resolver, id))
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		w.Header().Set("ETag", etag)
		w.Header().Set(xKSPAuthorityHeader, kspAuthorityLabel(resolver, id))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(manifest)
	}
}

// getKBFile handles GET /api/v1/kb/{kind}/{id}/file?path=<path> (KSP §4.2).
// Returns file bytes with ETag: <digest>, X-KSP-Authority, and Last-Modified.
// 404 if the path is not in the serving node's manifest.
//
// When sync is disabled (the default) the route returns 501 with
// X-KSP-Enabled: false. An unregistered kind returns 404.
func getKBFile(srv kbView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := chi.URLParam(r, "kind")
		id := chi.URLParam(r, "id")

		// Disabled ⇒ 501 (KSP §8).
		if !srv.KBSyncEnabled() {
			w.Header().Set(xKSPEnabledHeader, "false")
			writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errKBDisabled})
			return
		}

		// Unregistered kind ⇒ 404 (KSP §2.1).
		resolver := srv.KBResolveScope(kind)
		if resolver == nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown knowledgebase scope kind: " + kind})
			return
		}

		// Validate the scope id.
		if err := resolver.Validate(id); err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBScopeNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}

		// Authorization (KSP §9).
		if err := resolver.Authorize(r, id, false); err != nil {
			writeKBAuthzError(w, err)
			return
		}

		// Path query parameter (KSP §4.2).
		relPath := r.URL.Query().Get("path")
		if relPath == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errKBMissingPath})
			return
		}

		// Read the file from the serving tree.
		data, digest, modTime, err := resolver.ReadFile(id, relPath)
		if err != nil {
			if errors.Is(err, server.ErrKBFileNotFound) || isOSNotExist(err) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBFileNotFound})
				return
			}
			if strings.Contains(err.Error(), "kb: invalid path") {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: errKBInvalidPath})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "read file: " + err.Error()})
			return
		}

		etag := "\"" + digest + "\""
		w.Header().Set("ETag", etag)
		w.Header().Set(xKSPAuthorityHeader, kspAuthorityLabel(resolver, id))
		w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		//nolint:gosec // G705: file content is the KB document the caller authorized
		_, _ = w.Write(data)
	}
}

// matchesETag checks whether an If-None-Match header value matches the digest.
// Handles both quoted and unquoted forms, and the weak-etag prefix W/"...".
func matchesETag(headerVal, digest string) bool {
	// Strip weak prefix if present.
	val := strings.TrimPrefix(headerVal, "W/")
	// Handle multiple tags (comma-separated per RFC 7232).
	for _, tag := range strings.Split(val, ",") {
		tag = strings.TrimSpace(tag)
		tag = strings.Trim(tag, "\"")
		if tag == "*" || tag == digest {
			return true
		}
	}
	return false
}

// writeKBAuthzError maps a KB authorization error to an HTTP response.
// Forbidden → 403; unknown scope → 404; anything else → 500.
func writeKBAuthzError(w http.ResponseWriter, err error) {
	if errors.Is(err, server.ErrKBForbidden) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "forbidden"})
		return
	}
	if errors.Is(err, server.ErrProjectNotFound) {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBScopeNotFound})
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
}

// isOSNotExist reports whether err is an fs.ErrNotExist (file not found).
func isOSNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}

// putKBFile handles PUT /api/v1/kb/{kind}/{id}/file?path=<path> (KSP §4.3).
// Write a file with mandatory compare-and-swap:
//   - If-Match: <digest> — apply only if the current canonical digest matches.
//   - If-None-Match: * — apply only if the path does not exist (create).
//
// Neither header ⇒ 428 Precondition Required. On a participant, the request
// forwards to the authority with CAS headers preserved (412 passed through
// verbatim). The authority serializes concurrent writes per path, writes via
// temp+rename, and verifies the body against Content-Digest when present.
//
//nolint:gocyclo // KSP §4.3 CAS write — complex by spec
func putKBFile(srv kbView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := chi.URLParam(r, "kind")
		id := chi.URLParam(r, "id")

		if !srv.KBSyncEnabled() {
			w.Header().Set(xKSPEnabledHeader, "false")
			writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errKBDisabled})
			return
		}

		resolver := srv.KBResolveScope(kind)
		if resolver == nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown knowledgebase scope kind: " + kind})
			return
		}

		if err := resolver.Validate(id); err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBScopeNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}

		// Authorization (KSP §9): writes require write authority.
		if err := resolver.Authorize(r, id, true); err != nil {
			writeKBAuthzError(w, err)
			return
		}

		// Path query parameter.
		relPath := r.URL.Query().Get("path")
		if relPath == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errKBMissingPath})
			return
		}

		// On a participant, forward to the authority (KSP §4.3: every node is a
		// write entry point; a participant forwards and returns 412 verbatim).
		if !resolver.IsAuthority(id) {
			forwardKBWrite(srv, w, r, kind, id)
			return
		}

		// CAS precondition: If-Match or If-None-Match:* is required (KSP §4.3).
		ifMatch := r.Header.Get("If-Match")
		ifNoneMatch := r.Header.Get("If-None-Match")
		if ifMatch == "" && ifNoneMatch == "" {
			writeJSON(w, http.StatusPreconditionRequired, errorResponse{Error: "If-Match or If-None-Match is required"})
			return
		}

		// Read the body (bounded by max_file_size, enforced below).
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "read body: " + err.Error()})
			return
		}
		_ = r.Body.Close()

		// Size cap (KSP §4.3: 413 over the size cap).
		policy := resolver.Policy()
		maxSize := policy.MaxFileSize
		if maxSize == 0 {
			maxSize = server.DefaultKBMaxFileSize
		}
		if int64(len(body)) > maxSize {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "file exceeds size cap"})
			return
		}

		// Per-path serialization (KSP §4.3: serialize concurrent writes per path).
		mu := srv.(*server.Server).KBWriteLock(kind, id, relPath)
		mu.Lock()
		defer mu.Unlock()

		// Read the current canonical digest for CAS evaluation.
		_, currentDigest, _, readErr := resolver.ReadFile(id, relPath)
		fileExists := readErr == nil

		// If-None-Match: * — create only (path must not exist).
		if ifNoneMatch == "*" {
			if fileExists {
				writeJSON(w, http.StatusConflict, errorResponse{Error: "path already exists"})
				return
			}
		} else if ifMatch != "" {
			// If-Match: <digest> — apply only if the current digest matches.
			if !fileExists {
				writeJSON(w, http.StatusPreconditionFailed, errorResponse{Error: "file does not exist"})
				return
			}
			// Strip quotes/weak prefix for comparison.
			stripped := strings.TrimPrefix(ifMatch, "W/")
			stripped = strings.Trim(stripped, "\"")
			if stripped != currentDigest {
				// 412 + current entry (KSP §4.3, §6).
				w.Header().Set("ETag", "\""+currentDigest+"\"")
				writeJSON(w, http.StatusPreconditionFailed, errorResponse{Error: errKBPrecondition})
				return
			}
		}

		// No-op success: writing identical content (KSP §4.3).
		if fileExists && currentDigest != "" {
			bodyDigest := sha256Hex(body)
			if bodyDigest == currentDigest {
				w.Header().Set("ETag", "\""+currentDigest+"\"")
				w.Header().Set(xKSPAuthorityHeader, "authority")
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		// Write via temp+rename (KSP §4.3).
		newDigest, err := resolver.WriteFile(id, relPath, body)
		if err != nil {
			if strings.Contains(err.Error(), "kb: invalid path") {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: errKBInvalidPath})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "write file: " + err.Error()})
			return
		}

		// Invalidate the manifest cache so the next read reflects the change.
		resolver.InvalidateCache(id)

		w.Header().Set("ETag", "\""+newDigest+"\"")
		w.Header().Set(xKSPAuthorityHeader, "authority")
		w.WriteHeader(http.StatusOK)
	}
}

// deleteKBFile handles DELETE /api/v1/kb/{kind}/{id}/file?path=<path> (KSP §4.4).
// Delete a file. If-Match: <digest> is required; 412 on mismatch, 404 if absent.
// On a participant, the request forwards to the authority.
//
//nolint:gocyclo // KSP §4.4 CAS delete — complex by spec
func deleteKBFile(srv kbView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := chi.URLParam(r, "kind")
		id := chi.URLParam(r, "id")

		if !srv.KBSyncEnabled() {
			w.Header().Set(xKSPEnabledHeader, "false")
			writeJSON(w, http.StatusNotImplemented, errorResponse{Error: errKBDisabled})
			return
		}

		resolver := srv.KBResolveScope(kind)
		if resolver == nil {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "unknown knowledgebase scope kind: " + kind})
			return
		}

		if err := resolver.Validate(id); err != nil {
			if errors.Is(err, server.ErrProjectNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBScopeNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}

		// Authorization: writes require write authority.
		if err := resolver.Authorize(r, id, true); err != nil {
			writeKBAuthzError(w, err)
			return
		}

		// Path query parameter.
		relPath := r.URL.Query().Get("path")
		if relPath == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errKBMissingPath})
			return
		}

		// On a participant, forward to the authority.
		if !resolver.IsAuthority(id) {
			forwardKBWrite(srv, w, r, kind, id)
			return
		}

		// CAS precondition: If-Match is required (KSP §4.4).
		ifMatch := r.Header.Get("If-Match")
		if ifMatch == "" {
			writeJSON(w, http.StatusPreconditionRequired, errorResponse{Error: "If-Match is required"})
			return
		}

		// Per-path serialization.
		mu := srv.(*server.Server).KBWriteLock(kind, id, relPath)
		mu.Lock()
		defer mu.Unlock()

		// Read the current canonical digest for CAS evaluation.
		_, currentDigest, _, readErr := resolver.ReadFile(id, relPath)
		if readErr != nil {
			if errors.Is(readErr, server.ErrKBFileNotFound) || isOSNotExist(readErr) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBFileNotFound})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "read file: " + readErr.Error()})
			return
		}

		// If-Match: <digest> — apply only if the current digest matches.
		stripped := strings.TrimPrefix(ifMatch, "W/")
		stripped = strings.Trim(stripped, "\"")
		if stripped != currentDigest {
			w.Header().Set("ETag", "\""+currentDigest+"\"")
			writeJSON(w, http.StatusPreconditionFailed, errorResponse{Error: errKBPrecondition})
			return
		}

		// Delete the file.
		if err := resolver.DeleteFile(id, relPath); err != nil {
			if errors.Is(err, server.ErrKBFileNotFound) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: errKBFileNotFound})
				return
			}
			if strings.Contains(err.Error(), "kb: invalid path") {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: errKBInvalidPath})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "delete file: " + err.Error()})
			return
		}

		// Invalidate the manifest cache.
		resolver.InvalidateCache(id)

		w.Header().Set(xKSPAuthorityHeader, "authority")
		w.WriteHeader(http.StatusOK)
	}
}

// forwardKBWrite forwards a PUT/DELETE request to the authority, preserving
// CAS headers and returning the authority's response (including 412) verbatim
// (KSP §4.3, §4.4).
func forwardKBWrite(srv kbView, w http.ResponseWriter, r *http.Request, _, _ string) {
	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "read request body: " + err.Error()})
			return
		}
		_ = r.Body.Close()
	}

	uid, _ := resolveForwardedUser(r)
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}

	status, header, respBody, err := srv.ForwardKBRequest(r.Context(), r.Method, path, body, r.Header, uid)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorResponse{Error: "forward to leader: " + err.Error()})
		return
	}

	for k, vs := range header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	//nolint:gosec // G705: respBody is the authority's trusted API response
	_, _ = w.Write(respBody)
}

// sha256Hex returns the "sha256:<hex>" digest of data, for the no-op-identical
// content check in the PUT handler.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}
