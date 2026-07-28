package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectStore_Create_SetsOwner(t *testing.T) {
	ps := newProjectStore()
	p, err := ps.Create(CreateProjectInput{
		Name:       "owned",
		AgentNames: []string{"greeter"},
		Owner:      "alice",
	})
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Owner)

	// Empty owner is preserved (auth disabled ⇒ unowned).
	p2, err := ps.Create(CreateProjectInput{Name: "noauth", AgentNames: []string{"greeter"}})
	require.NoError(t, err)
	assert.Empty(t, p2.Owner)
}

func TestProjectStore_AddUser(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}, Owner: "alice"})

	updated, err := ps.AddUser(p.ID, "bob")
	require.NoError(t, err)
	require.Len(t, updated.Team.Users, 1)
	assert.Equal(t, "bob", updated.Team.Users[0].UserID)

	// Idempotent: adding Bob again is a no-op.
	updated, err = ps.AddUser(p.ID, "bob")
	require.NoError(t, err)
	assert.Len(t, updated.Team.Users, 1)

	// Adding a second user appends.
	updated, err = ps.AddUser(p.ID, "carol")
	require.NoError(t, err)
	require.Len(t, updated.Team.Users, 2)
}

func TestProjectStore_AddUser_NotFound(t *testing.T) {
	ps := newProjectStore()
	_, err := ps.AddUser("nope", "bob")
	assert.ErrorIs(t, err, ErrProjectNotFound)
}

func TestProjectStore_RemoveUser(t *testing.T) {
	ps := newProjectStore()
	p, _ := ps.Create(CreateProjectInput{Name: "p", AgentNames: []string{"greeter"}, Owner: "alice"})
	_, _ = ps.AddUser(p.ID, "bob")
	_, _ = ps.AddUser(p.ID, "carol")

	updated, err := ps.RemoveUser(p.ID, "bob")
	require.NoError(t, err)
	require.Len(t, updated.Team.Users, 1)
	assert.Equal(t, "carol", updated.Team.Users[0].UserID)

	// Idempotent: removing a non-member is a no-op (still 200, one entry left).
	updated, err = ps.RemoveUser(p.ID, "bob")
	require.NoError(t, err)
	assert.Len(t, updated.Team.Users, 1)
}

func TestProjectStore_RemoveUser_NotFound(t *testing.T) {
	ps := newProjectStore()
	_, err := ps.RemoveUser("nope", "bob")
	assert.ErrorIs(t, err, ErrProjectNotFound)
}
