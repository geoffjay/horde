package server

// SessionStore derives and tracks session keys for agent invocations. The
// session key (agent_id:project_id) determines conversation continuity across
// invocations — a stable key retains history; a fresh key starts a new
// conversation.
//
// The actual conversation history is stored inside the agent subprocess by
// the ADK runner's session service (InMemoryService today; a database-backed
// service in the future). This interface is the server-side seam: it knows
// which agent is in which project and derives the key. A future
// implementation could persist key mappings or track active sessions for
// observability.
//
// See the persistence decision doc and the project/team model decision for
// the session-key contract.
type SessionStore interface {
	// SessionKey derives the session key for the given agent. Returns ""
	// when the agent has no active project (the caller falls back to
	// per-invocation sessions).
	SessionKey(agentID string) string
}

// Compile-time: *Server satisfies SessionStore.
var _ SessionStore = (*Server)(nil)
