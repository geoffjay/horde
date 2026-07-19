package server

import (
	"io"

	"github.com/hashicorp/raft"
)

// raftFSM adapts the Server's replicated state to the raft.FSM interface. It
// delegates to a raftFSMHandler (the Server) so this file stays free of the
// project/resume specifics. When handler is nil (election-only mode, before the
// state stores are wired through raft) every method is a safe no-op.
type raftFSM struct {
	handler raftFSMHandler
}

// Apply is invoked on every node as each committed log entry is applied. The
// return value is surfaced to the leader's raft.Apply caller via Future.Response
// — an error is returned so the caller can distinguish an application failure
// from a replication success.
func (f *raftFSM) Apply(l *raft.Log) any {
	if f.handler == nil {
		return nil
	}
	res, err := f.handler.applyCommand(l.Data)
	if err != nil {
		return err
	}
	return res
}

// Snapshot captures the current replicated state so raft can compact the log.
func (f *raftFSM) Snapshot() (raft.FSMSnapshot, error) {
	if f.handler == nil {
		return &raftSnapshot{}, nil
	}
	data, err := f.handler.snapshotState()
	if err != nil {
		return nil, err
	}
	return &raftSnapshot{data: data}, nil
}

// Restore replaces the replicated state from a snapshot (on startup or when a
// follower falls too far behind).
func (f *raftFSM) Restore(rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()
	if f.handler == nil {
		_, _ = io.Copy(io.Discard, rc)
		return nil
	}
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	return f.handler.restoreState(data)
}

// raftSnapshot is a raft.FSMSnapshot backed by a byte buffer.
type raftSnapshot struct {
	data []byte
}

// Persist writes the snapshot bytes to the sink and closes it.
func (s *raftSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

// Release is a no-op; the buffer is garbage-collected.
func (s *raftSnapshot) Release() {}
