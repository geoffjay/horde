package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirupsen/logrus"
)

// resumeStore persists the latest AAP resume_token per agent so a respawned
// adapter (or a node restart) can resume the prior conversation instead of
// starting cold. It is keyed by the config agent name — stable across
// respawns, and a named AAP agent has at most one running instance. When path
// is empty it is in-memory only (tests), mirroring the project store.
//
// Under raft failover (apply/isLeader set) the tokens are replicated through the
// raft log instead of a per-node file, so a respawn on a newly-elected leader
// resumes the conversation. Replication is keyed by agent name (cluster-stable,
// node-agnostic) so the new leader — a different node — finds the token; a name
// collision across nodes shares the token, which is the intended "same logical
// agent" semantics. A resume-set that happens on a follower (an AAP agent placed
// off the leader) falls back to a node-local write, matching pre-failover
// per-node behavior — that is the documented boundary.
type resumeStore struct {
	mu     sync.Mutex
	tokens map[string]string
	path   string // persistence file; empty = in-memory only

	// apply / isLeader are set under raft failover so set() replicates through
	// the log when this node leads. Nil when failover is off.
	apply    raftApply
	isLeader func() bool
}

// resumeCommand is one replicated resume-token update.
type resumeCommand struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// newResumeStore builds the store, loading any persisted tokens when path is
// set. A missing file is not an error (fresh start).
func newResumeStore(path string) *resumeStore {
	rs := &resumeStore{tokens: make(map[string]string), path: path}
	rs.load()
	return rs
}

func (rs *resumeStore) load() {
	if rs.path == "" {
		return
	}
	data, err := os.ReadFile(rs.path)
	if err != nil {
		if !os.IsNotExist(err) {
			logrus.WithError(err).Warn("failed to load aap resume tokens; starting fresh")
		}
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		logrus.WithError(err).Warn("failed to parse aap resume tokens; starting fresh")
		return
	}
	rs.mu.Lock()
	rs.tokens = m
	rs.mu.Unlock()
}

// get returns the persisted resume token for the named agent, or "" if none.
func (rs *resumeStore) get(name string) string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.tokens[name]
}

// set records (and persists) the resume token for the named agent. A no-op
// when the token is unchanged, to avoid needless flushes. Under raft failover,
// when this node leads, the update is replicated through the log (the FSM
// updates every replica's map); otherwise it is written node-locally.
func (rs *resumeStore) set(name, token string) {
	rs.mu.Lock()
	unchanged := rs.tokens[name] == token
	rs.mu.Unlock()
	if unchanged {
		return
	}

	if rs.apply != nil && rs.isLeader != nil && rs.isLeader() {
		data, err := encodeRaftCommand(raftKindResume, resumeCommand{Name: name, Token: token})
		if err == nil {
			if _, err = rs.apply(data); err == nil {
				return // the FSM applied it to the map on every replica
			}
		}
		logrus.WithError(err).Debug("resume: raft replicate failed; writing node-locally")
	}

	rs.mu.Lock()
	rs.tokens[name] = token
	rs.flushLocked()
	rs.mu.Unlock()
}

// applyCommand applies a replicated resume-token update to the in-memory map. It
// is invoked from the raft FSM (single-threaded) on every replica. No file write
// — under failover the raft log/snapshots are the source of truth.
func (rs *resumeStore) applyCommand(data []byte) error {
	var cmd resumeCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		return err
	}
	rs.mu.Lock()
	rs.tokens[cmd.Name] = cmd.Token
	rs.mu.Unlock()
	return nil
}

// snapshot returns a copy of the token map for a raft snapshot.
func (rs *resumeStore) snapshot() map[string]string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make(map[string]string, len(rs.tokens))
	for k, v := range rs.tokens {
		out[k] = v
	}
	return out
}

// restore replaces the token map from a raft snapshot.
func (rs *resumeStore) restore(m map[string]string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if m == nil {
		m = make(map[string]string)
	}
	rs.tokens = m
}

// flushLocked writes the current tokens to disk. The caller must hold rs.mu.
func (rs *resumeStore) flushLocked() {
	if rs.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(rs.path), stateDirPerm); err != nil {
		logrus.WithError(err).Warn("aap resume: create state dir")
		return
	}
	data, err := json.MarshalIndent(rs.tokens, "", "  ")
	if err != nil {
		logrus.WithError(err).Warn("aap resume: marshal tokens")
		return
	}
	if err := os.WriteFile(rs.path, data, stateFilePerm); err != nil {
		logrus.WithError(err).Warn("aap resume: write tokens")
	}
}
