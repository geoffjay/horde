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
type resumeStore struct {
	mu     sync.Mutex
	tokens map[string]string
	path   string // persistence file; empty = in-memory only
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
// when the token is unchanged, to avoid needless flushes.
func (rs *resumeStore) set(name, token string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.tokens[name] == token {
		return
	}
	rs.tokens[name] = token
	rs.flushLocked()
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
