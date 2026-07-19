package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedTime is a stable timestamp for deterministic-replay assertions.
func fixedTime() time.Time { return time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC) }

// wireLocalApply wires a raftProjectStore's replication path to apply commands
// synchronously to its own FSM, simulating a single-node raft where every
// command commits and applies immediately. It exercises the full
// encode → envelope → applyCommand → mem-mutation path.
func wireLocalApply(rs *raftProjectStore) {
	rs.apply = func(data []byte) (any, error) {
		var env raftCommand
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, err
		}
		return rs.applyCommand(env.Data)
	}
}

func TestRaftProjectStore_CRUDThroughLog(t *testing.T) {
	rs := newRaftProjectStore()
	wireLocalApply(rs)

	// Create → deterministic id, active, team from names.
	p, err := rs.Create(CreateProjectInput{Name: "alpha", Goal: "ship", AgentNames: []string{"greeter"}})
	require.NoError(t, err)
	assert.Equal(t, "proj-1", p.ID)
	assert.Equal(t, ProjectActive, p.State)
	require.Len(t, p.Team.Agents, 1)
	assert.Equal(t, "greeter", p.Team.Agents[0].Name)

	// A second create advances the deterministic id counter.
	p2, err := rs.Create(CreateProjectInput{Name: "beta", AgentNames: []string{"greeter"}})
	require.NoError(t, err)
	assert.Equal(t, "proj-2", p2.ID)

	// Get / List read the applied state.
	got, err := rs.Get("proj-1")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name)
	assert.Len(t, rs.List(""), 2)

	// UpdateState.
	upd, err := rs.UpdateState("proj-1", ProjectPaused)
	require.NoError(t, err)
	assert.Equal(t, ProjectPaused, upd.State)

	// Assign then remove an agent (assign requires an active project).
	_, err = rs.UpdateState("proj-1", ProjectActive)
	require.NoError(t, err)
	asg, err := rs.AssignAgent("proj-1", "a1", "greeter")
	require.NoError(t, err)
	assert.Len(t, asg.Team.Agents, 2)
	rm, err := rs.RemoveAgent("proj-1", "a1")
	require.NoError(t, err)
	assert.Len(t, rm.Team.Agents, 1)

	// Delete.
	require.NoError(t, rs.Delete("proj-2"))
	_, err = rs.Get("proj-2")
	assert.ErrorIs(t, err, ErrProjectNotFound)
}

func TestRaftProjectStore_InvalidCreateNotReplicated(t *testing.T) {
	rs := newRaftProjectStore()
	applied := 0
	rs.apply = func(data []byte) (any, error) {
		applied++
		var env raftCommand
		_ = json.Unmarshal(data, &env)
		return rs.applyCommand(env.Data)
	}

	// Missing agents → validated before replication (apply never called).
	_, err := rs.Create(CreateProjectInput{Name: "x"})
	require.Error(t, err)
	assert.Zero(t, applied, "invalid input must not be replicated")
}

func TestRaftProjectStore_DeterministicAcrossReplicas(t *testing.T) {
	// The same command bytes applied to two independent stores (as raft replays
	// the log on every replica) must yield byte-identical projects.
	cmd := projectCommand{Op: opCreate, Now: fixedTime(), Name: "same", Agents: []string{"greeter"}}
	data, err := json.Marshal(cmd)
	require.NoError(t, err)

	a := newRaftProjectStore()
	ra, err := a.applyCommand(data)
	require.NoError(t, err)
	b := newRaftProjectStore()
	rb, err := b.applyCommand(data)
	require.NoError(t, err)

	pa, _ := json.Marshal(ra)
	pb, _ := json.Marshal(rb)
	assert.JSONEq(t, string(pa), string(pb), "replicas must apply a command identically")
}

func TestRaftProjectStore_SnapshotRestore(t *testing.T) {
	rs := newRaftProjectStore()
	wireLocalApply(rs)
	_, err := rs.Create(CreateProjectInput{Name: "alpha", AgentNames: []string{"greeter"}})
	require.NoError(t, err)
	_, err = rs.Create(CreateProjectInput{Name: "beta", AgentNames: []string{"greeter"}})
	require.NoError(t, err)

	snap := rs.mem.snapshot()

	// Restore into a fresh store and confirm state + the id counter carry over.
	restored := newRaftProjectStore()
	wireLocalApply(restored)
	restored.mem.restore(snap)

	got, err := restored.Get("proj-1")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name)
	assert.Len(t, restored.List(""), 2)

	// nextID preserved → the next create is proj-3, not proj-1.
	p, err := restored.Create(CreateProjectInput{Name: "gamma", AgentNames: []string{"greeter"}})
	require.NoError(t, err)
	assert.Equal(t, "proj-3", p.ID)
}
