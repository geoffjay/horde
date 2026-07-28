package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/geoffjay/horde/internal/client"
)

func TestRenderUsersView_Disabled(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	out := m.renderUsersView()
	assert.Contains(t, out, "auth is disabled")
	assert.NotContains(t, out, "alice")
}

func TestRenderUsersView_EnabledListsUsers(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.users = client.UsersResponse{
		AuthEnabled: true,
		Users: []client.User{
			{ID: "alice", Admin: false, You: true},
			{ID: "bob", Admin: true, You: false},
		},
	}
	out := m.renderUsersView()
	assert.Contains(t, out, "auth enabled")
	assert.Contains(t, out, "alice")
	assert.Contains(t, out, "(you)")
	assert.Contains(t, out, "bob")
	assert.Contains(t, out, "admin")
}

func TestRenderUsersView_EnabledNoUsers(t *testing.T) {
	m := newTestModel("127.0.0.1:1")
	m.users = client.UsersResponse{AuthEnabled: true}
	out := m.renderUsersView()
	assert.Contains(t, out, "auth enabled")
	assert.Contains(t, out, "no users configured")
}
