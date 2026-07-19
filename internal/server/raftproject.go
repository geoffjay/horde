package server

import (
	"encoding/json"
	"fmt"
	"time"
)

// projectOp identifies a replicated project mutation.
type projectOp string

const (
	opCreate      projectOp = "create"
	opUpdateState projectOp = "update_state"
	opAssignAgent projectOp = "assign_agent"
	opRemoveAgent projectOp = "remove_agent"
	opDelete      projectOp = "delete"
)

// projectCommand is one replicated project mutation. The leader resolves any
// non-deterministic values (the timestamp) before replicating so every replica
// applies the same result; the project id is assigned deterministically by the
// FSM from the replicated nextID counter.
type projectCommand struct {
	Op        projectOp    `json:"op"`
	Now       time.Time    `json:"now"`
	Name      string       `json:"name,omitempty"`
	Workspace string       `json:"workspace,omitempty"`
	Goal      string       `json:"goal,omitempty"`
	Agents    []string     `json:"agents,omitempty"`
	ID        string       `json:"id,omitempty"`
	State     ProjectState `json:"state,omitempty"`
	AgentID   string       `json:"agent_id,omitempty"`
	AgentName string       `json:"agent_name,omitempty"`
}

// raftApply replicates a command through the raft log and returns the FSM
// response. It is implemented by the Server (over raftNode); a seam so the
// raftProjectStore is testable without a running raft.
type raftApply func(data []byte) (any, error)

// raftProjectStore is a ProjectStore whose mutations are replicated through the
// raft log: each mutation is encoded as a projectCommand, applied via raft (so
// it is committed on a quorum and replayed by every replica's FSM), and the
// FSM's response is returned to the caller. Reads are served from the locally
// applied in-memory state (mem). A newly-elected leader therefore has current
// project state without a separate replication path.
//
// Mutations only succeed on the raft leader; on a follower raft.Apply returns
// ErrNotLeader. That never happens in practice because the API forwards project
// requests to the leader (projectForwardMiddleware), but it is the safety net.
type raftProjectStore struct {
	mem   *memProjectStore
	apply raftApply
}

// newRaftProjectStore builds a raft-backed store over an in-memory applied state
// (no disk path — the raft log/snapshots are the source of truth). apply is
// wired by the Server once the raft node exists.
func newRaftProjectStore() *raftProjectStore {
	return &raftProjectStore{mem: &memProjectStore{
		projects: make(map[string]*Project),
		now:      time.Now,
	}}
}

func (rs *raftProjectStore) Get(id string) (*Project, error) { return rs.mem.Get(id) }

func (rs *raftProjectStore) List(stateFilter ProjectState) []Project {
	return rs.mem.List(stateFilter)
}

func (rs *raftProjectStore) Create(in CreateProjectInput) (*Project, error) {
	if err := validateCreateInput(in); err != nil {
		return nil, err
	}
	return rs.applyProject(&projectCommand{
		Op:        opCreate,
		Now:       time.Now().UTC(),
		Name:      in.Name,
		Workspace: in.Workspace,
		Goal:      in.Goal,
		Agents:    in.AgentNames,
	})
}

func (rs *raftProjectStore) UpdateState(id string, state ProjectState) (*Project, error) {
	return rs.applyProject(&projectCommand{Op: opUpdateState, Now: time.Now().UTC(), ID: id, State: state})
}

func (rs *raftProjectStore) AssignAgent(id, agentID, agentName string) (*Project, error) {
	return rs.applyProject(&projectCommand{
		Op: opAssignAgent, Now: time.Now().UTC(), ID: id, AgentID: agentID, AgentName: agentName,
	})
}

func (rs *raftProjectStore) RemoveAgent(id, agentID string) (*Project, error) {
	return rs.applyProject(&projectCommand{Op: opRemoveAgent, Now: time.Now().UTC(), ID: id, AgentID: agentID})
}

func (rs *raftProjectStore) Delete(id string) error {
	_, err := rs.replicate(&projectCommand{Op: opDelete, ID: id})
	return err
}

// replicate encodes a project command and applies it through the raft log,
// returning the raw FSM response.
func (rs *raftProjectStore) replicate(cmd *projectCommand) (any, error) {
	if rs.apply == nil {
		return nil, fmt.Errorf("raft project store: not wired to raft")
	}
	data, err := encodeRaftCommand(raftKindProject, cmd)
	if err != nil {
		return nil, err
	}
	return rs.apply(data)
}

// applyProject replicates a mutation whose FSM response is the affected project
// (create/update-state/assign/remove).
func (rs *raftProjectStore) applyProject(cmd *projectCommand) (*Project, error) {
	resp, err := rs.replicate(cmd)
	if err != nil {
		return nil, err
	}
	p, ok := resp.(*Project)
	if !ok {
		return nil, fmt.Errorf("raft project store: unexpected apply response %T", resp)
	}
	return p, nil
}

// applyCommand applies a decoded project command to the in-memory state. It is
// invoked from the FSM (single-threaded) on every replica. The returned value
// is surfaced to the leader's Apply caller.
func (rs *raftProjectStore) applyCommand(data []byte) (any, error) {
	var cmd projectCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		return nil, fmt.Errorf("raft project store: decode command: %w", err)
	}
	rs.mem.mu.Lock()
	defer rs.mem.mu.Unlock()
	switch cmd.Op {
	case opCreate:
		return rs.mem.createLocked(CreateProjectInput{
			Name: cmd.Name, Workspace: cmd.Workspace, Goal: cmd.Goal, AgentNames: cmd.Agents,
		}, cmd.Now)
	case opUpdateState:
		return rs.mem.updateStateLocked(cmd.ID, cmd.State, cmd.Now)
	case opAssignAgent:
		return rs.mem.assignAgentLocked(cmd.ID, cmd.AgentID, cmd.AgentName, cmd.Now)
	case opRemoveAgent:
		return rs.mem.removeAgentLocked(cmd.ID, cmd.AgentID, cmd.Now)
	case opDelete:
		return nil, rs.mem.deleteLocked(cmd.ID)
	default:
		return nil, fmt.Errorf("raft project store: unknown op %q", cmd.Op)
	}
}
