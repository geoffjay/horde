package aap

import (
	"encoding/json"
	"strings"
)

// SystemPromptMode selects whether a system prompt replaces or appends to the
// agent's default.
type SystemPromptMode string

const (
	// SystemPromptReplace replaces the agent's default system prompt.
	SystemPromptReplace SystemPromptMode = "replace"
	// SystemPromptAppend appends to the agent's default (capability
	// system_prompt_append).
	SystemPromptAppend SystemPromptMode = "append"
)

// PermissionMode is the file-system access mode of a [Permissions] scope.
type PermissionMode string

const (
	// PermissionReadOnly forbids all writes.
	PermissionReadOnly PermissionMode = "read_only"
	// PermissionReadWrite allows writes, optionally narrowed by writable paths.
	PermissionReadWrite PermissionMode = "read_write"
)

// ActivityState is the agent activity reported via [Status].
type ActivityState string

const (
	// StateBusy indicates the agent is working a turn.
	StateBusy ActivityState = "busy"
	// StateIdle indicates the agent is waiting for input.
	StateIdle ActivityState = "idle"
)

// LogLevel is the severity of a [Log] line.
type LogLevel string

const (
	// LogInfo is informational severity.
	LogInfo LogLevel = "info"
	// LogWarn is warning severity.
	LogWarn LogLevel = "warn"
	// LogError is error severity.
	LogError LogLevel = "error"
)

// ApprovalDecision is the host's decision on a tool-use approval.
type ApprovalDecision string

const (
	// DecisionAllow permits the tool call.
	DecisionAllow ApprovalDecision = "allow"
	// DecisionDeny rejects the tool call.
	DecisionDeny ApprovalDecision = "deny"
)

// Content block "type" tags.
const (
	// BlockText is visible assistant output.
	BlockText = "text"
	// BlockThinking is agent reasoning (capability thinking).
	BlockThinking = "thinking"
)

// SystemPrompt configures the agent's system prompt. Exactly one of Text or
// Path is set.
type SystemPrompt struct {
	Mode SystemPromptMode `json:"mode"`
	Text *string          `json:"text,omitempty"`
	Path *string          `json:"path,omitempty"`
}

// Workspace describes the directories the agent operates in.
type Workspace struct {
	// Cwd is the working directory (required).
	Cwd string `json:"cwd"`
	// AdditionalDirs are extra directories the agent may access.
	AdditionalDirs []string `json:"additional_dirs,omitempty"`
	// Worktree requests an isolated worktree if the adapter supports it.
	Worktree bool `json:"worktree,omitempty"`
}

// Tools provisions tool access for the agent.
type Tools struct {
	// MCPServers are MCP server definitions keyed by name (capability mcp).
	MCPServers map[string]MCPServer `json:"mcp_servers,omitempty"`
}

// MCPServer is a single MCP server definition (stdio transport).
type MCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Permissions is the up-front file-system permission scope carried in
// [Initialize] (capability permissions). A compliant adapter self-enforces it
// independent of tool approval.
type Permissions struct {
	// Mode is the access mode (required within permissions).
	Mode PermissionMode `json:"mode"`
	// WritablePaths narrows writes when Mode is read_write. Empty means the
	// whole workspace is writable.
	WritablePaths []string `json:"writable_paths,omitempty"`
	// DenyPaths are paths the agent must not read or write, overriding the rest.
	DenyPaths []string `json:"deny_paths,omitempty"`
}

// AgentInfo identifies the agent behind an adapter, reported in [Ready].
type AgentInfo struct {
	Name    string  `json:"name"`
	Version *string `json:"version,omitempty"`
}

// Usage is the token/cost/timing accounting for a completed turn. Every field
// is always serialized (defaulting to zero) to match the host's usage snapshot
// shape.
type Usage struct {
	InputTokens              uint64  `json:"input_tokens"`
	OutputTokens             uint64  `json:"output_tokens"`
	CacheReadInputTokens     uint64  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens uint64  `json:"cache_creation_input_tokens"`
	TotalCostUSD             float64 `json:"total_cost_usd"`
	NumTurns                 uint64  `json:"num_turns"`
	DurationMS               uint64  `json:"duration_ms"`
	DurationAPIMS            uint64  `json:"duration_api_ms"`
}

// ContentBlock is one block of assistant content. Type is [BlockText] or
// [BlockThinking].
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TextBlock builds a visible-output content block.
func TextBlock(text string) ContentBlock { return ContentBlock{Type: BlockText, Text: text} }

// ThinkingBlock builds a reasoning content block.
func ThinkingBlock(text string) ContentBlock { return ContentBlock{Type: BlockThinking, Text: text} }

// PromptContent is the content of a [Prompt]: either a plain string or an
// ordered list of content blocks. It serializes untagged — a JSON string or a
// JSON array — matching the wire format.
type PromptContent struct {
	text   string
	blocks []ContentBlock
	isText bool
}

// TextPrompt builds prompt content from a plain string.
func TextPrompt(s string) PromptContent { return PromptContent{text: s, isText: true} }

// BlockPrompt builds prompt content from content blocks.
func BlockPrompt(blocks ...ContentBlock) PromptContent {
	return PromptContent{blocks: blocks}
}

// IsText reports whether the content is a plain string (rather than blocks).
func (c PromptContent) IsText() bool { return c.isText }

// Blocks returns the content blocks, or nil when the content is a plain string.
func (c PromptContent) Blocks() []ContentBlock { return c.blocks }

// AsText flattens the content into a single plain-text string, concatenating
// text blocks and skipping thinking blocks.
func (c PromptContent) AsText() string {
	if c.isText {
		return c.text
	}
	var b strings.Builder
	for _, block := range c.blocks {
		if block.Type == BlockText {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

// MarshalJSON implements json.Marshaler, emitting a string or a block array.
func (c PromptContent) MarshalJSON() ([]byte, error) {
	if c.isText {
		return json.Marshal(c.text)
	}
	return json.Marshal(c.blocks)
}

// UnmarshalJSON implements json.Unmarshaler, accepting a string or a block
// array.
func (c *PromptContent) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*c = PromptContent{text: s, isText: true}
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	*c = PromptContent{blocks: blocks}
	return nil
}
