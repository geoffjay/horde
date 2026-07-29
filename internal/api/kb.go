package api

import (
	"encoding/json"
	"errors"
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
			writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "knowledgebase sync is disabled"})
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
			writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "knowledgebase sync is disabled"})
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
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "missing path query parameter"})
			return
		}

		// Read the file from the serving tree.
		data, digest, modTime, err := resolver.ReadFile(id, relPath)
		if err != nil {
			if errors.Is(err, server.ErrKBFileNotFound) || isOSNotExist(err) {
				writeJSON(w, http.StatusNotFound, errorResponse{Error: "file not found in knowledgebase"})
				return
			}
			if strings.Contains(err.Error(), "kb: invalid path") {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid path"})
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
