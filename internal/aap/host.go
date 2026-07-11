package aap

import "encoding/json"

// Initialize is the first host message on a connection; it carries all agent
// configuration (H→A). It MUST precede every other host message.
type Initialize struct {
	// ProtocolVersion is the AAP version the host speaks. The adapter emits a
	// fatal Error if it cannot serve this version.
	ProtocolVersion int `json:"protocol_version"`
	// Model is the requested model; the adapter's default is used when nil.
	Model *string `json:"model,omitempty"`
	// SystemPrompt overrides or extends the agent's system prompt.
	SystemPrompt *SystemPrompt `json:"system_prompt,omitempty"`
	// Workspace is the directory configuration (required).
	Workspace Workspace `json:"workspace"`
	// Tools provisions tool access (capability mcp).
	Tools *Tools `json:"tools,omitempty"`
	// Permissions is the up-front file-system scope (capability permissions).
	Permissions *Permissions `json:"permissions,omitempty"`
	// ResumeToken resumes a prior conversation (capability resume).
	ResumeToken *string `json:"resume_token,omitempty"`
}

// Type implements [HostMessage].
func (Initialize) Type() string   { return TypeInitialize }
func (Initialize) isHostMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
//
//nolint:gocritic // value receiver required: encoding/json invokes MarshalJSON on non-addressable values held in an interface.
func (m Initialize) MarshalJSON() ([]byte, error) {
	type body Initialize
	return marshalTagged(TypeInitialize, body(m))
}

// Prompt begins a turn with user input (H→A).
type Prompt struct {
	// TurnID is a host-assigned id echoed on all output for the turn.
	TurnID string `json:"turn_id"`
	// Content is a plain string or an ordered list of content blocks.
	Content PromptContent `json:"content"`
}

// Type implements [HostMessage].
func (Prompt) Type() string   { return TypePrompt }
func (Prompt) isHostMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m Prompt) MarshalJSON() ([]byte, error) {
	type body Prompt
	return marshalTagged(TypePrompt, body(m))
}

// Cancel requests interruption of a turn (H→A, capability cancel). A nil TurnID
// cancels the current turn.
type Cancel struct {
	TurnID *string `json:"turn_id,omitempty"`
}

// Type implements [HostMessage].
func (Cancel) Type() string   { return TypeCancel }
func (Cancel) isHostMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m Cancel) MarshalJSON() ([]byte, error) {
	type body Cancel
	return marshalTagged(TypeCancel, body(m))
}

// ClearContext discards conversation history and starts fresh (H→A, capability
// context_clear).
type ClearContext struct{}

// Type implements [HostMessage].
func (ClearContext) Type() string   { return TypeClearContext }
func (ClearContext) isHostMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (ClearContext) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"type": TypeClearContext})
}

// Shutdown requests graceful termination of the adapter (H→A).
type Shutdown struct{}

// Type implements [HostMessage].
func (Shutdown) Type() string   { return TypeShutdown }
func (Shutdown) isHostMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (Shutdown) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"type": TypeShutdown})
}

// ApprovalResponse carries the host's decision on a pending tool-use approval
// (H→A).
type ApprovalResponse struct {
	// RequestID correlates with the ApprovalRequest.
	RequestID string `json:"request_id"`
	// Decision is allow or deny.
	Decision ApprovalDecision `json:"decision"`
	// UpdatedInput, when present, is an opaque replacement input the adapter
	// uses in place of the original tool input.
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
	// Message is a human-readable reason, typically included on denial.
	Message *string `json:"message,omitempty"`
}

// Type implements [HostMessage].
func (ApprovalResponse) Type() string   { return TypeApprovalResponse }
func (ApprovalResponse) isHostMessage() {}

// MarshalJSON implements json.Marshaler, injecting the "type" tag.
func (m ApprovalResponse) MarshalJSON() ([]byte, error) {
	type body ApprovalResponse
	return marshalTagged(TypeApprovalResponse, body(m))
}
