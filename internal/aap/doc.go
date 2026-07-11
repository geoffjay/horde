// Package aap implements the Agent Adapter Protocol (AAP) v1: the
// vendor-neutral, NDJSON wire protocol between a host (orchestrator) and an AI
// coding agent driven through an adapter process.
//
// The protocol is defined in docs/spec/agent-adapter-protocol-v1.md. This
// package provides the typed [HostMessage] and [AgentMessage] families that
// model both directions, plus (de)serialization that round-trips the wire
// format, a [RunMockAdapter] conformance fixture, and the shared test vectors
// in testdata/vectors.json.
//
// Messages are exchanged as newline-delimited JSON: one JSON object per line,
// discriminated by a top-level "type" field. Deserialization ignores unknown
// fields, and an unknown message type surfaces as [UnknownTypeError] so callers
// can log and skip the line rather than treat it as fatal.
package aap

// ProtocolVersion is the AAP version implemented by this package.
const ProtocolVersion = 1

// Environment variables carrying the transport binding an adapter should use.
// Hosts SHOULD set the canonical AAP_* names; the legacy AGENTD_AAP_* names are
// accepted as deprecated aliases when the canonical names are absent.
const (
	EnvTransport       = "AAP_TRANSPORT"
	EnvWSURL           = "AAP_WS_URL"
	LegacyEnvTransport = "AGENTD_AAP_TRANSPORT"
	LegacyEnvWSURL     = "AGENTD_AAP_WS_URL"
)

// Transport binding selectors (values of EnvTransport).
const (
	TransportStdio     = "stdio"
	TransportWebSocket = "websocket"
)

// Capability tokens advertised by an adapter in [Ready]. Capabilities are
// free-form strings on the wire so unknown tokens are ignored rather than
// rejected; these constants name the tokens this protocol version defines.
const (
	// CapStreaming indicates incremental message frames during a turn.
	CapStreaming = "streaming"
	// CapThinking indicates the adapter emits thinking content blocks.
	CapThinking = "thinking"
	// CapToolApproval indicates the approval_request/approval_response exchange.
	CapToolApproval = "tool_approval"
	// CapUsageReporting indicates usage token counts on turn_complete.
	CapUsageReporting = "usage_reporting"
	// CapCostReporting indicates usage.total_cost_usd is populated.
	CapCostReporting = "cost_reporting"
	// CapContextClear indicates the adapter handles clear_context.
	CapContextClear = "context_clear"
	// CapCancel indicates the adapter handles cancel.
	CapCancel = "cancel"
	// CapMCP indicates the adapter honors tools.mcp_servers.
	CapMCP = "mcp"
	// CapSystemPromptAppend indicates system_prompt.mode = "append" is supported.
	CapSystemPromptAppend = "system_prompt_append"
	// CapResume indicates the adapter emits and consumes resume_token.
	CapResume = "resume"
	// CapPermissions indicates the adapter self-enforces initialize.permissions.
	CapPermissions = "permissions"
	// CapExecutionContext indicates the adapter emits context frames reporting
	// its blocked / waiting-for-model / progress state.
	CapExecutionContext = "execution_context"
)

// TransportFromEnv resolves the transport binding from env, preferring the
// canonical AAP_TRANSPORT over the legacy AGENTD_AAP_TRANSPORT alias. lookup is
// an env accessor (e.g. os.LookupEnv). It returns TransportStdio when neither
// variable is set, matching the mandatory stdio baseline.
func TransportFromEnv(lookup func(string) (string, bool)) string {
	if v, ok := lookup(EnvTransport); ok && v != "" {
		return v
	}
	if v, ok := lookup(LegacyEnvTransport); ok && v != "" {
		return v
	}
	return TransportStdio
}

// WSURLFromEnv resolves the websocket URL from env, preferring the canonical
// AAP_WS_URL over the legacy AGENTD_AAP_WS_URL alias.
func WSURLFromEnv(lookup func(string) (string, bool)) string {
	if v, ok := lookup(EnvWSURL); ok && v != "" {
		return v
	}
	if v, ok := lookup(LegacyEnvWSURL); ok {
		return v
	}
	return ""
}
