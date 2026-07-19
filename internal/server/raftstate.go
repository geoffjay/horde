package server

import (
	"encoding/json"
	"fmt"
)

// raftKind tags a replicated command so the FSM can dispatch it to the right
// sub-applier. The master-only stores replicated through the raft log each get a
// kind (project state now; AAP resume tokens follow).
type raftKind string

const (
	raftKindProject raftKind = "project"
	raftKindResume  raftKind = "resume"
)

// raftCommand is the envelope replicated through the raft log. Data is the
// kind-specific command payload (e.g. a projectCommand).
type raftCommand struct {
	Kind raftKind        `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// encodeRaftCommand marshals a kind-specific command into the raft envelope.
func encodeRaftCommand(kind raftKind, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("raft: encode %s command: %w", kind, err)
	}
	return json.Marshal(raftCommand{Kind: kind, Data: data})
}

// raftSnapshotState is the full replicated state captured in a raft snapshot.
type raftSnapshotState struct {
	Projects projectStoreState `json:"projects"`
	Resume   map[string]string `json:"resume,omitempty"`
}

// raftHandler returns the FSM handler for replicated state, or nil when failover
// is off (the FSM then no-ops). Under failover the Server is the handler,
// dispatching commands to the raft-backed stores.
func (s *Server) raftHandler() raftFSMHandler {
	if s.cfg.Failover != FailoverRaft {
		return nil
	}
	return s
}

// applyCommand dispatches one replicated command to its sub-applier. Invoked by
// the FSM (single-threaded) on every replica.
func (s *Server) applyCommand(data []byte) (any, error) {
	var cmd raftCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		return nil, fmt.Errorf("raft: decode command envelope: %w", err)
	}
	switch cmd.Kind {
	case raftKindProject:
		if s.raftProjects == nil {
			return nil, fmt.Errorf("raft: project command with no project store")
		}
		return s.raftProjects.applyCommand(cmd.Data)
	case raftKindResume:
		return nil, s.resume.applyCommand(cmd.Data)
	default:
		return nil, fmt.Errorf("raft: unknown command kind %q", cmd.Kind)
	}
}

// snapshotState serializes the replicated state for a raft snapshot.
func (s *Server) snapshotState() ([]byte, error) {
	st := raftSnapshotState{}
	if s.raftProjects != nil {
		st.Projects = s.raftProjects.mem.snapshot()
	}
	if s.resume != nil {
		st.Resume = s.resume.snapshot()
	}
	return json.Marshal(st)
}

// restoreState replaces the replicated state from a raft snapshot.
func (s *Server) restoreState(data []byte) error {
	var st raftSnapshotState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("raft: decode snapshot: %w", err)
	}
	if s.raftProjects != nil {
		s.raftProjects.mem.restore(st.Projects)
	}
	if s.resume != nil {
		s.resume.restore(st.Resume)
	}
	return nil
}
