package aap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
)

// mockAgentName is the agent name the mock adapter reports in Ready.
const mockAgentName = "mock"

// mockTurnCount is the num_turns the mock reports for every turn.
const mockTurnCount = 1

// RunMockAdapter runs a deterministic AAP v1 adapter over the stdio binding: it
// reads host frames from in and writes agent frames to out. It completes the
// handshake, then answers each Prompt with a fixed Message and TurnComplete,
// acknowledges ClearContext, and returns on Shutdown or when in reaches EOF.
//
// It is a conformance fixture and a worked reference for adapter authors, not a
// real agent. ctx is honored between frames; a blocked read unblocks when in is
// closed.
func RunMockAdapter(ctx context.Context, in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	ok, err := mockHandshake(r, out)
	if err != nil || !ok {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line, err := ReadLine(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		msg, perr := ParseHostMessage(line)
		if perr != nil {
			var unknown *UnknownTypeError
			if errors.As(perr, &unknown) {
				continue // per spec: log and skip unknown types
			}
			return perr
		}
		stop, herr := mockHandle(out, msg)
		if herr != nil {
			return herr
		}
		if stop {
			return nil
		}
	}
}

// mockHandshake reads the initialize frame and replies with ready. When the
// first frame is not a supported initialize it emits a fatal error frame and
// returns ok=false so the adapter exits. err is set only on an I/O failure.
func mockHandshake(r *bufio.Reader, out io.Writer) (ok bool, err error) {
	line, err := ReadLine(r)
	if err != nil {
		return false, err
	}
	msg, err := ParseHostMessage(line)
	if err != nil {
		return false, err
	}
	init, isInit := msg.(Initialize)
	if !isInit {
		return false, WriteMessage(out, Error{
			Fatal:   true,
			Code:    strptr("unexpected_message"),
			Message: "expected initialize as the first frame",
		})
	}
	if init.ProtocolVersion != ProtocolVersion {
		return false, WriteMessage(out, Error{
			Fatal:   true,
			Code:    strptr("unsupported_version"),
			Message: fmt.Sprintf("mock adapter speaks protocol version %d", ProtocolVersion),
		})
	}
	return true, WriteMessage(out, Ready{
		ProtocolVersion: ProtocolVersion,
		Agent:           AgentInfo{Name: mockAgentName},
		Capabilities:    []string{CapStreaming, CapCancel, CapContextClear, CapUsageReporting},
	})
}

// mockHandle dispatches one host message, returning stop=true when the adapter
// should exit.
func mockHandle(out io.Writer, msg HostMessage) (stop bool, err error) {
	switch m := msg.(type) {
	case Prompt:
		return false, mockAnswer(out, m)
	case ClearContext:
		return false, WriteMessage(out, Log{Level: LogInfo, Message: "context cleared"})
	case Shutdown:
		return true, nil
	default:
		// Cancel, ApprovalResponse, a repeated Initialize: nothing to do.
		return false, nil
	}
}

// mockAnswer produces the deterministic reply to a prompt: busy, one echoed
// message, turn_complete with usage, idle.
func mockAnswer(out io.Writer, p Prompt) error {
	if err := WriteMessage(out, Status{State: StateBusy}); err != nil {
		return err
	}
	reply := "mock: " + p.Content.AsText()
	if err := WriteMessage(out, Message{
		TurnID:  p.TurnID,
		Content: []ContentBlock{TextBlock(reply)},
	}); err != nil {
		return err
	}
	usage := Usage{NumTurns: mockTurnCount}
	if err := WriteMessage(out, TurnComplete{
		TurnID:     p.TurnID,
		StopReason: strptr("end_turn"),
		ResultText: &reply,
		Usage:      &usage,
	}); err != nil {
		return err
	}
	return WriteMessage(out, Status{State: StateIdle})
}

// strptr returns a pointer to s, for optional string fields.
func strptr(s string) *string { return &s }
