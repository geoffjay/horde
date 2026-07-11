package aap

import "encoding/json"

// Ready is emitted once, after the adapter has started its native agent and is
// prepared to accept prompts (A→H). The host MUST NOT send a Prompt before it.
type Ready struct {
	ProtocolVersion int       `json:"protocol_version"`
	Agent           AgentInfo `json:"agent"`
	// Capabilities are the capability tokens the adapter supports (§7).
	Capabilities []string `json:"capabilities"`
	// Models are the models the adapter can serve, if it advertises them.
	Models []string `json:"models,omitempty"`
}

// Type implements [AgentMessage].
func (Ready) Type() string    { return TypeReady }
func (Ready) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
//
//nolint:gocritic // value receiver required: encoding/json invokes MarshalJSON on non-addressable values held in an interface.
func (m Ready) MarshalJSON() ([]byte, error) {
	type body Ready
	return marshalTagged(TypeReady, body(m))
}

// Message carries assistant output blocks for a turn (A→H). Adapters may stream
// multiple Message frames per turn (capability streaming).
type Message struct {
	TurnID  string         `json:"turn_id"`
	Content []ContentBlock `json:"content"`
}

// Type implements [AgentMessage].
func (Message) Type() string    { return TypeMessage }
func (Message) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m Message) MarshalJSON() ([]byte, error) {
	type body Message
	return marshalTagged(TypeMessage, body(m))
}

// ToolCall announces a tool invocation (A→H). CallID is unique within the turn
// and correlates with an ApprovalRequest, if any.
type ToolCall struct {
	TurnID string `json:"turn_id"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	// Input is the opaque native tool input.
	Input json.RawMessage `json:"input,omitempty"`
}

// Type implements [AgentMessage].
func (ToolCall) Type() string    { return TypeToolCall }
func (ToolCall) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m ToolCall) MarshalJSON() ([]byte, error) {
	type body ToolCall
	return marshalTagged(TypeToolCall, body(m))
}

// TurnComplete marks the end of a turn (A→H). Usage is present only with the
// usage_reporting capability; ResumeToken only with resume.
type TurnComplete struct {
	TurnID      string  `json:"turn_id"`
	IsError     bool    `json:"is_error"`
	StopReason  *string `json:"stop_reason,omitempty"`
	ResultText  *string `json:"result_text,omitempty"`
	ResumeToken *string `json:"resume_token,omitempty"`
	Usage       *Usage  `json:"usage,omitempty"`
}

// Type implements [AgentMessage].
func (TurnComplete) Type() string    { return TypeTurnComplete }
func (TurnComplete) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m TurnComplete) MarshalJSON() ([]byte, error) {
	type body TurnComplete
	return marshalTagged(TypeTurnComplete, body(m))
}

// Status reports an activity transition (A→H).
type Status struct {
	State ActivityState `json:"state"`
}

// Type implements [AgentMessage].
func (Status) Type() string    { return TypeStatus }
func (Status) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m Status) MarshalJSON() ([]byte, error) {
	type body Status
	return marshalTagged(TypeStatus, body(m))
}

// Log is a structured diagnostic line (A→H).
type Log struct {
	Level   LogLevel `json:"level"`
	Message string   `json:"message"`
}

// Type implements [AgentMessage].
func (Log) Type() string    { return TypeLog }
func (Log) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m Log) MarshalJSON() ([]byte, error) {
	type body Log
	return marshalTagged(TypeLog, body(m))
}

// ApprovalRequest asks the host to approve a tool call (A→H, capability
// tool_approval). Correlated by RequestID.
type ApprovalRequest struct {
	RequestID string  `json:"request_id"`
	CallID    *string `json:"call_id,omitempty"`
	ToolName  string  `json:"tool_name"`
	// Input is the opaque native tool input under review.
	Input json.RawMessage `json:"input,omitempty"`
}

// Type implements [AgentMessage].
func (ApprovalRequest) Type() string    { return TypeApprovalRequest }
func (ApprovalRequest) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m ApprovalRequest) MarshalJSON() ([]byte, error) {
	type body ApprovalRequest
	return marshalTagged(TypeApprovalRequest, body(m))
}

// Error reports an adapter error (A→H). Fatal indicates the adapter is exiting.
type Error struct {
	Fatal   bool    `json:"fatal"`
	Code    *string `json:"code,omitempty"`
	Message string  `json:"message"`
}

// Type implements [AgentMessage].
func (Error) Type() string    { return TypeError }
func (Error) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m Error) MarshalJSON() ([]byte, error) {
	type body Error
	return marshalTagged(TypeError, body(m))
}

// ContextUpdate is the agent's self-reported execution context (A→H, capability
// execution_context; wire tag "context"). Every field is optional: a frame is a
// partial update the host merges over the last known state. The host owns
// project/issue assignment; the agent only refines Issue and reports runtime
// fields.
type ContextUpdate struct {
	// TurnID is the turn this update pertains to, if any.
	TurnID *string `json:"turn_id,omitempty"`
	// Issue is the agent's refinement of the issue it is working.
	Issue *string `json:"issue,omitempty"`
	// Blocked reports the agent cannot proceed without external input.
	Blocked *bool `json:"blocked,omitempty"`
	// BlockedReason explains a blocked state.
	BlockedReason *string `json:"blocked_reason,omitempty"`
	// WaitingModel reports the agent is awaiting a model response.
	WaitingModel *bool `json:"waiting_model,omitempty"`
	// Note is short free-text progress.
	Note *string `json:"note,omitempty"`
}

// Type implements [AgentMessage].
func (ContextUpdate) Type() string    { return TypeContext }
func (ContextUpdate) isAgentMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m ContextUpdate) MarshalJSON() ([]byte, error) {
	type body ContextUpdate
	return marshalTagged(TypeContext, body(m))
}
