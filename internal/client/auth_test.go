package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetAuth_SendsBearerOnUnaryRequest(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"master","leader_connected":true,"node_id":"n1","version":"test"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.Listener.Addr().String())
	c.SetAuth("tok-123")
	_, err := c.Node(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer tok-123", gotAuth)
}

func TestSetAuth_EmptyLeavesNoHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mode":"master","leader_connected":true,"node_id":"n1","version":"test"}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.Listener.Addr().String())
	_, err := c.Node(context.Background())
	require.NoError(t, err)
	assert.Empty(t, gotAuth, "no token ⇒ no Authorization header")
}

// TestSetAuth_SendsBearerOnStreamingRequests asserts the bearer token is
// attached to every streaming request path, not just the unary send.
func TestSetAuth_SendsBearerOnStreamingRequests(t *testing.T) {
	cases := []struct {
		name string
		call func(ctx context.Context, c *Client) error
	}{
		{"Invoke", func(ctx context.Context, c *Client) error {
			_, err := c.Invoke(ctx, "agent-1", InvokeRequest{Message: "hi"})
			return err
		}},
		{"StreamEvents", func(ctx context.Context, c *Client) error {
			_, err := c.StreamEvents(ctx)
			return err
		}},
		{"StreamAgentContext", func(ctx context.Context, c *Client) error {
			_, err := c.StreamAgentContext(ctx, "agent-1")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				// Return immediately; the stream ends and the client's
				// initial call returns after seeing the 200.
			}))
			t.Cleanup(srv.Close)

			c := New(srv.Listener.Addr().String())
			c.SetAuth("tok-123")
			require.NoError(t, tc.call(context.Background(), c))
			assert.Equal(t, "Bearer tok-123", gotAuth)
		})
	}
}

func TestUsers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/users", r.URL.Path)
		_, _ = w.Write([]byte(`{"auth_enabled":true,"users":[{"id":"alice","admin":false,"you":true},{"id":"bob","admin":true,"you":false}]}`))
	}))
	t.Cleanup(srv.Close)

	c := New(srv.Listener.Addr().String())
	out, err := c.Users(context.Background())
	require.NoError(t, err)
	assert.True(t, out.AuthEnabled)
	require.Len(t, out.Users, 2)
	assert.Equal(t, "alice", out.Users[0].ID)
	assert.True(t, out.Users[0].You)
	assert.True(t, out.Users[1].Admin)
}
