package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// clusterAuthView is the subset of *server.Server the cluster-auth middleware
// needs.
type clusterAuthView interface {
	ClusterAuthToken() string
}

// requireClusterAuth verifies the shared cluster bearer token on node→node
// ingest endpoints (register / heartbeat / events). When no token is configured
// it passes through — auth is disabled and the cluster behaves as before
// (backward compatible). A missing or mismatched token yields 401. The compare
// is constant-time to avoid leaking the token via timing.
func requireClusterAuth(srv clusterAuthView) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := srv.ClusterAuthToken()
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}
			presented := bearerToken(r.Header.Get("Authorization"))
			if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid cluster auth token"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header, returning empty when the header is absent or malformed.
func bearerToken(h string) string {
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}
