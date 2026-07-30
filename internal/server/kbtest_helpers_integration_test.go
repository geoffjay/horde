//go:build integration

package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// newKBOnlyRouter creates a minimal HTTP handler that serves only the KB
// manifest and file routes. This is for integration tests in the server
// package that need to serve KB content without importing internal/api (which
// would create an import cycle). It mirrors the logic in
// internal/api/kb.go:getKBManifest and getKBFile.
func newKBOnlyRouter(srv *Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/kb/", func(w http.ResponseWriter, r *http.Request) {
		// Parse /api/v1/kb/{kind}/{id}/{manifest|file}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/kb/"), "/")
		if len(parts) < 3 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		kind, id, action := parts[0], parts[1], parts[2]

		if !srv.KBSyncEnabled() {
			w.Header().Set("X-KSP-Enabled", "false")
			http.Error(w, "knowledgebase sync is disabled", http.StatusNotImplemented)
			return
		}
		resolver := srv.KBResolveScope(kind)
		if resolver == nil {
			http.Error(w, "unknown scope kind", http.StatusNotFound)
			return
		}
		if err := resolver.Validate(id); err != nil {
			http.Error(w, "scope not found", http.StatusNotFound)
			return
		}

		scope := KBScopeRef{Kind: kind, ID: id}
		switch action {
		case "manifest":
			manifest, err := resolver.CachedManifest(scope)
			if err != nil {
				http.Error(w, fmt.Sprintf("scan: %s", err), http.StatusInternalServerError)
				return
			}
			etag := "\"" + manifest.ManifestDigest + "\""
			if inm := r.Header.Get("If-None-Match"); inm != "" {
				if strings.Contains(inm, manifest.ManifestDigest) {
					w.Header().Set("ETag", etag)
					w.Header().Set("X-KSP-Authority", "authority")
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
			w.Header().Set("ETag", etag)
			w.Header().Set("X-KSP-Authority", "authority")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(manifest)

		case "file":
			relPath := r.URL.Query().Get("path")
			if relPath == "" {
				http.Error(w, "missing path param", http.StatusBadRequest)
				return
			}
			switch r.Method {
			case http.MethodGet:
				data, digest, _, err := resolver.ReadFile(id, relPath)
				if err != nil {
					http.Error(w, "file not found", http.StatusNotFound)
					return
				}
				w.Header().Set("ETag", digest)
				w.Header().Set("X-KSP-Authority", "authority")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)

			case http.MethodPut:
				handleTestPutFile(srv, w, r, resolver, id, relPath)

			case http.MethodDelete:
				handleTestDeleteFile(srv, w, r, resolver, id, relPath)

			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}

		case "conflicts":
			conflicts, err := srv.KBConflicts(kind, id)
			if err != nil {
				http.Error(w, fmt.Sprintf("list conflicts: %s", err), http.StatusInternalServerError)
				return
			}
			w.Header().Set("X-KSP-Authority", "authority")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(conflicts)

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})
	return mux
}

// handleTestPutFile handles PUT on the test KB router — the authority-side CAS
// write, mirroring internal/api/kb.go:putKBFile. It serializes per path,
// evaluates If-Match/If-None-Match, writes via temp+rename, and returns the
// ETag. Used by the stage-2 push integration tests.
func handleTestPutFile(srv *Server, w http.ResponseWriter, r *http.Request, resolver ScopeResolver, id, relPath string) {
	ifMatch := r.Header.Get("If-Match")
	ifNoneMatch := r.Header.Get("If-None-Match")
	if ifMatch == "" && ifNoneMatch == "" {
		http.Error(w, "If-Match or If-None-Match is required", http.StatusPreconditionRequired)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	policy := resolver.Policy()
	maxSize := policy.MaxFileSize
	if maxSize == 0 {
		maxSize = DefaultKBMaxFileSize
	}
	if int64(len(body)) > maxSize {
		http.Error(w, "file exceeds size cap", http.StatusRequestEntityTooLarge)
		return
	}

	mu := srv.KBWriteLock("project", id, relPath)
	mu.Lock()
	defer mu.Unlock()

	_, currentDigest, _, readErr := resolver.ReadFile(id, relPath)
	fileExists := readErr == nil

	if ifNoneMatch == "*" {
		if fileExists {
			http.Error(w, "path already exists", http.StatusConflict)
			return
		}
	} else if ifMatch != "" {
		if !fileExists {
			http.Error(w, "file does not exist", http.StatusPreconditionFailed)
			return
		}
		stripped := strings.TrimPrefix(ifMatch, "W/")
		stripped = strings.Trim(stripped, "\"")
		if stripped != currentDigest {
			w.Header().Set("ETag", "\""+currentDigest+"\"")
			http.Error(w, "precondition failed", http.StatusPreconditionFailed)
			return
		}
	}

	if fileExists && currentDigest != "" {
		bodyDigest := testSha256Hex(body)
		if bodyDigest == currentDigest {
			w.Header().Set("ETag", "\""+currentDigest+"\"")
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	newDigest, err := resolver.WriteFile(id, relPath, body)
	if err != nil {
		http.Error(w, "write file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resolver.InvalidateCache(id)

	w.Header().Set("ETag", "\""+newDigest+"\"")
	w.Header().Set("X-KSP-Authority", "authority")
	w.WriteHeader(http.StatusOK)
}

// handleTestDeleteFile handles DELETE on the test KB router — the authority-
// side CAS delete, mirroring internal/api/kb.go:deleteKBFile.
func handleTestDeleteFile(srv *Server, w http.ResponseWriter, r *http.Request, resolver ScopeResolver, id, relPath string) {
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		http.Error(w, "If-Match is required", http.StatusPreconditionRequired)
		return
	}

	mu := srv.KBWriteLock("project", id, relPath)
	mu.Lock()
	defer mu.Unlock()

	_, currentDigest, _, readErr := resolver.ReadFile(id, relPath)
	if readErr != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	stripped := strings.TrimPrefix(ifMatch, "W/")
	stripped = strings.Trim(stripped, "\"")
	if stripped != currentDigest {
		w.Header().Set("ETag", "\""+currentDigest+"\"")
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}

	if err := resolver.DeleteFile(id, relPath); err != nil {
		http.Error(w, "delete file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resolver.InvalidateCache(id)

	w.Header().Set("X-KSP-Authority", "authority")
	w.WriteHeader(http.StatusOK)
}

// testSha256Hex returns the "sha256:<hex>" digest of data.
func testSha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return kbDigestPrefix + hex.EncodeToString(h[:])
}

// newLeaderClientForTest creates a leaderClient with a static discoverer that
// resolves to the given address. This lets integration tests point a
// participant's convergence loop at a test HTTP server.
func newLeaderClientForTest(addr, nodeID, token string) *leaderClient {
	return newLeaderClient(&staticDiscoverer{addr: addr}, nodeID, "", token)
}
