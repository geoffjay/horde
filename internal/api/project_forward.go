package api

import (
	"io"
	"net/http"
)

// projectForwardMiddleware intercepts project API requests on a slave node
// with a configured leader and forwards them to the master. On a master or
// standalone slave (no leader), requests pass through to the local handlers.
//
// The master is the source of truth for project state: a slave that receives
// a project request proxies it to the master and returns the master's
// response. This keeps project state cluster-wide without active replication.
func projectForwardMiddleware(fwd projectForwarder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if fwd == nil || fwd.LeaderAddr() == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Read the request body (if any) for forwarding.
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

			// Forward to the master. The path includes the /api/v1 prefix
			// (chi routes are nested under /api/v1, but the full path is
			// available on the request).
			status, header, respBody, err := fwd.ForwardProjectRequest(r.Context(), r.Method, r.URL.Path+queryString(r), body)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, errorResponse{Error: "forward to leader: " + err.Error()})
				return
			}

			// Copy the master's response back to the caller.
			for k, vs := range header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(status)
			//nolint:gosec // G705: respBody is the master's trusted API response, not user input
			_, _ = w.Write(respBody)
		})
	}
}

// queryString returns the query string (with leading "?") from r.URL.RawQuery,
// or "" when empty.
func queryString(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return ""
	}
	return "?" + r.URL.RawQuery
}
