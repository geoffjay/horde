package aap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// TurnObserver receives the frames streamed by an adapter during a turn and
// decides tool-use approvals. Every field is optional; nil callbacks are
// skipped and a nil Approve denies by default (fail-safe).
type TurnObserver struct {
	// OnMessage receives assistant message frames (text/thinking blocks).
	OnMessage func(Message)
	// OnToolCall receives tool invocation announcements.
	OnToolCall func(ToolCall)
	// OnStatus receives busy/idle transitions.
	OnStatus func(Status)
	// OnLog receives diagnostic log lines (and non-fatal errors).
	OnLog func(Log)
	// OnContext receives execution-context updates.
	OnContext func(ContextUpdate)
	// Approve decides a tool-use approval. When nil the session denies every
	// request (the safe default for an unattended driver).
	Approve func(ApprovalRequest) ApprovalDecision
}

// HostSession drives a single AAP adapter connection from the host side over
// the stdio binding: it writes host frames to w (the adapter's stdin) and
// reads agent frames from r (the adapter's stdout). It implements the host
// half of the lifecycle in the spec (§8): initialize → ready → prompt → turn
// → shutdown.
//
// A HostSession is not safe for concurrent use; drive one turn at a time.
type HostSession struct {
	r *bufio.Reader
	w io.Writer
}

// NewHostSession wraps an adapter's stdout (r) and stdin (w).
func NewHostSession(r io.Reader, w io.Writer) *HostSession {
	return &HostSession{r: bufio.NewReader(r), w: w}
}

// Initialize sends the initialize frame and waits for the adapter's ready
// frame, tolerating log frames that precede it. A fatal error frame before
// ready aborts the handshake.
func (s *HostSession) Initialize(init *Initialize) (*Ready, error) {
	if init.ProtocolVersion == 0 {
		init.ProtocolVersion = ProtocolVersion
	}
	if err := WriteMessage(s.w, init); err != nil {
		return nil, fmt.Errorf("aap: send initialize: %w", err)
	}
	for {
		msg, err := s.read()
		if err != nil {
			return nil, err
		}
		switch m := msg.(type) {
		case Ready:
			return &m, nil
		case Error:
			if m.Fatal {
				return nil, fmt.Errorf("aap: adapter error before ready: %s", m.Message)
			}
		default:
			// Ignore pre-ready logs and any other frames (spec is lenient here).
		}
	}
}

// Prompt sends a prompt and drives the turn to completion, invoking obs for
// each streamed frame and answering approval requests via obs.Approve. It
// returns the closing turn_complete frame.
func (s *HostSession) Prompt(turnID string, content PromptContent, obs TurnObserver) (*TurnComplete, error) {
	if err := WriteMessage(s.w, Prompt{TurnID: turnID, Content: content}); err != nil {
		return nil, fmt.Errorf("aap: send prompt: %w", err)
	}
	for {
		msg, err := s.read()
		if err != nil {
			return nil, err
		}
		switch m := msg.(type) {
		case ApprovalRequest:
			decision := DecisionDeny
			if obs.Approve != nil {
				decision = obs.Approve(m)
			}
			if err := WriteMessage(s.w, ApprovalResponse{RequestID: m.RequestID, Decision: decision}); err != nil {
				return nil, fmt.Errorf("aap: send approval_response: %w", err)
			}
		case Error:
			if m.Fatal {
				return nil, fmt.Errorf("aap: fatal adapter error: %s", m.Message)
			}
			if obs.OnLog != nil {
				obs.OnLog(Log{Level: LogError, Message: m.Message})
			}
		case TurnComplete:
			// Accept the turn's own id (or an unlabelled frame) as the close.
			if m.TurnID == turnID || m.TurnID == "" {
				return &m, nil
			}
		default:
			// Streamed, non-control frames (message/tool_call/status/log/context).
			obs.observe(msg)
		}
	}
}

// observe dispatches the streamed, non-control frames to the observer's
// callbacks. Unknown or control frames are ignored here.
func (o TurnObserver) observe(msg AgentMessage) {
	switch m := msg.(type) {
	case Message:
		if o.OnMessage != nil {
			o.OnMessage(m)
		}
	case ToolCall:
		if o.OnToolCall != nil {
			o.OnToolCall(m)
		}
	case Status:
		if o.OnStatus != nil {
			o.OnStatus(m)
		}
	case Log:
		if o.OnLog != nil {
			o.OnLog(m)
		}
	case ContextUpdate:
		if o.OnContext != nil {
			o.OnContext(m)
		}
	}
}

// Shutdown asks the adapter to terminate gracefully.
func (s *HostSession) Shutdown() error {
	return WriteMessage(s.w, Shutdown{})
}

// read returns the next typed agent frame, skipping unknown message types per
// the spec (§3: log and skip rather than fail).
func (s *HostSession) read() (AgentMessage, error) {
	for {
		line, err := ReadLine(s.r)
		if err != nil {
			return nil, err
		}
		msg, perr := ParseAgentMessage(line)
		if perr != nil {
			var unk *UnknownTypeError
			if errors.As(perr, &unk) {
				continue
			}
			return nil, perr
		}
		return msg, nil
	}
}
