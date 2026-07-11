package aap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Message type tags. These are the values of the top-level "type" field that
// discriminates every AAP frame.
const (
	// Host → Agent.
	TypeInitialize       = "initialize"
	TypePrompt           = "prompt"
	TypeCancel           = "cancel"
	TypeClearContext     = "clear_context"
	TypeShutdown         = "shutdown"
	TypeApprovalResponse = "approval_response"

	// Agent → Host.
	TypeReady           = "ready"
	TypeMessage         = "message"
	TypeToolCall        = "tool_call"
	TypeTurnComplete    = "turn_complete"
	TypeStatus          = "status"
	TypeLog             = "log"
	TypeApprovalRequest = "approval_request"
	TypeError           = "error"
)

// HostMessage is a message sent by the host (orchestrator) to the agent
// adapter. The concrete variants are [Initialize], [Prompt], [Cancel],
// [ClearContext], [Shutdown], and [ApprovalResponse].
type HostMessage interface {
	// Type returns the wire "type" tag of the message.
	Type() string
	isHostMessage()
}

// AgentMessage is a message sent by the agent adapter to the host. The concrete
// variants are [Ready], [Message], [ToolCall], [TurnComplete], [Status], [Log],
// [ApprovalRequest], and [Error].
type AgentMessage interface {
	// Type returns the wire "type" tag of the message.
	Type() string
	isAgentMessage()
}

// UnknownTypeError is returned by [ParseHostMessage] and [ParseAgentMessage]
// when a frame carries a "type" the parser does not recognize. Per the spec a
// receiver should log and skip such a line rather than fail.
type UnknownTypeError struct {
	Type string
}

func (e *UnknownTypeError) Error() string {
	return fmt.Sprintf("aap: unknown message type %q", e.Type)
}

// hostDecoders and agentDecoders map a wire "type" tag to a decoder producing
// the concrete typed message. Using a table keeps ParseHostMessage /
// ParseAgentMessage flat instead of a large type switch.
var hostDecoders = map[string]func([]byte) (HostMessage, error){
	TypeInitialize:       decodeHost[Initialize],
	TypePrompt:           decodeHost[Prompt],
	TypeCancel:           decodeHost[Cancel],
	TypeClearContext:     decodeHost[ClearContext],
	TypeShutdown:         decodeHost[Shutdown],
	TypeApprovalResponse: decodeHost[ApprovalResponse],
}

var agentDecoders = map[string]func([]byte) (AgentMessage, error){
	TypeReady:           decodeAgent[Ready],
	TypeMessage:         decodeAgent[Message],
	TypeToolCall:        decodeAgent[ToolCall],
	TypeTurnComplete:    decodeAgent[TurnComplete],
	TypeStatus:          decodeAgent[Status],
	TypeLog:             decodeAgent[Log],
	TypeApprovalRequest: decodeAgent[ApprovalRequest],
	TypeError:           decodeAgent[Error],
}

func decodeHost[T HostMessage](data []byte) (HostMessage, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func decodeAgent[T AgentMessage](data []byte) (AgentMessage, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

// ParseHostMessage decodes one NDJSON frame into a typed [HostMessage]. An
// unrecognized "type" yields an [UnknownTypeError].
func ParseHostMessage(data []byte) (HostMessage, error) {
	t, err := peekType(data)
	if err != nil {
		return nil, err
	}
	dec, ok := hostDecoders[t]
	if !ok {
		return nil, &UnknownTypeError{Type: t}
	}
	return dec(data)
}

// ParseAgentMessage decodes one NDJSON frame into a typed [AgentMessage]. An
// unrecognized "type" yields an [UnknownTypeError].
func ParseAgentMessage(data []byte) (AgentMessage, error) {
	t, err := peekType(data)
	if err != nil {
		return nil, err
	}
	dec, ok := agentDecoders[t]
	if !ok {
		return nil, &UnknownTypeError{Type: t}
	}
	return dec(data)
}

// peekType extracts the "type" discriminator from a frame without decoding the
// rest of it.
func peekType(data []byte) (string, error) {
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return "", fmt.Errorf("aap: decode type tag: %w", err)
	}
	if env.Type == "" {
		return "", fmt.Errorf("aap: frame is missing a %q field", "type")
	}
	return env.Type, nil
}

// marshalTagged serializes body and injects the top-level "type" tag. Callers
// pass an alias of their struct (a type whose methods are stripped) as body to
// avoid recursing back into their own MarshalJSON.
func marshalTagged(typ string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	tag, err := json.Marshal(typ)
	if err != nil {
		return nil, err
	}
	fields["type"] = tag
	return json.Marshal(fields)
}

// WriteMessage writes m as a single NDJSON line (compact JSON followed by a
// newline) to w. m is typically a [HostMessage] or [AgentMessage].
func WriteMessage(w io.Writer, m any) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	raw = append(bytes.TrimRight(raw, "\n"), '\n')
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("aap: write frame: %w", err)
	}
	return nil
}

// ReadLine reads one NDJSON frame from r, skipping empty and whitespace-only
// lines per the spec. It returns io.EOF when the stream ends. The returned
// bytes exclude the trailing newline.
func ReadLine(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			return trimmed, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
