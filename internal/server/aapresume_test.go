package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// wireResumeLocalApply routes a resume store's replication through its own FSM
// synchronously, simulating a single-node raft where every command applies
// immediately.
func wireResumeLocalApply(rs *resumeStore, leader bool) {
	rs.isLeader = func() bool { return leader }
	rs.apply = func(data []byte) (any, error) {
		var env raftCommand
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, err
		}
		return nil, rs.applyCommand(env.Data)
	}
}

func TestResumeStore_ReplicatesThroughLogWhenLeader(t *testing.T) {
	rs := newResumeStore("")
	wireResumeLocalApply(rs, true)

	rs.set("coder", "tok-1")
	assert.Equal(t, "tok-1", rs.get("coder"))

	// An unchanged token is a no-op; a new value replaces it.
	rs.set("coder", "tok-1")
	rs.set("coder", "tok-2")
	assert.Equal(t, "tok-2", rs.get("coder"))
}

func TestResumeStore_FallsBackToLocalWhenNotLeader(t *testing.T) {
	rs := newResumeStore("")
	applied := 0
	rs.isLeader = func() bool { return false }
	rs.apply = func([]byte) (any, error) { applied++; return nil, nil }

	// Not the leader → set writes node-locally, never replicates.
	rs.set("coder", "tok-local")
	assert.Equal(t, "tok-local", rs.get("coder"))
	assert.Zero(t, applied, "a non-leader must not replicate")
}

func TestResumeStore_SnapshotRestore(t *testing.T) {
	rs := newResumeStore("")
	wireResumeLocalApply(rs, true)
	rs.set("a", "ta")
	rs.set("b", "tb")

	snap := rs.snapshot()

	restored := newResumeStore("")
	restored.restore(snap)
	assert.Equal(t, "ta", restored.get("a"))
	assert.Equal(t, "tb", restored.get("b"))

	// The snapshot is a copy: mutating the store does not change it.
	rs.set("a", "changed")
	assert.Equal(t, "ta", snap["a"])
}
