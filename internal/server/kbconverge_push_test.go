package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestKBClientPush_AttributesUserViaHeader verifies that a stage-2 watcher push
// echoes the configured PushUser as X-Horde-User so the authority can attribute
// the write and apply the scope's write authority (KSP §9). With no PushUser
// configured, no attribution header is sent — the push carries no write
// identity and fails closed once per-user auth is enabled.
func TestKBClientPush_AttributesUserViaHeader(t *testing.T) {
	var gotUser, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get(kbForwardedUserHeader)
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("ETag", `"sha256:deadbeef"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")
	scope := KBScopeRef{Kind: "project", ID: "p1"}

	// With a push user configured, the push echoes X-Horde-User and the
	// cluster token.
	c := newKBClient("cluster-tok", "svc-user")
	if _, status, err := c.putFile(context.Background(), addr, scope, "index.md", []byte("x"), "", "*"); err != nil || status != http.StatusOK {
		t.Fatalf("putFile: status=%d err=%v", status, err)
	}
	if gotUser != "svc-user" {
		t.Errorf("X-Horde-User = %q, want %q", gotUser, "svc-user")
	}
	if gotAuth != "Bearer cluster-tok" {
		t.Errorf("Authorization = %q, want cluster token", gotAuth)
	}

	// A delete push carries the same attribution.
	gotUser = "sentinel"
	if _, err := c.deleteFile(context.Background(), addr, scope, "index.md", "sha256:deadbeef"); err != nil {
		t.Fatalf("deleteFile: %v", err)
	}
	if gotUser != "svc-user" {
		t.Errorf("delete X-Horde-User = %q, want %q", gotUser, "svc-user")
	}

	// With no push user, no attribution header is sent (fail-closed under auth).
	gotUser = "sentinel"
	c2 := newKBClient("cluster-tok", "")
	if _, _, err := c2.putFile(context.Background(), addr, scope, "index.md", []byte("x"), "", "*"); err != nil {
		t.Fatalf("putFile(no user): %v", err)
	}
	if gotUser != "" {
		t.Errorf("X-Horde-User = %q, want empty (no push user)", gotUser)
	}
}
