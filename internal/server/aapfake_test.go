package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/geoffjay/horde/internal/aap"
)

// fakeAAPAdapter is a test-only AAP adapter that emits the full frame set
// (status, message, tool_call, approval_request, context, turn_complete)
// so the host's context/approval wiring can be exercised. It speaks the
// stdio binding on in/out, drives the initialize→ready handshake, and answers
// each prompt with a deterministic scripted sequence.
//
// It is intentionally richer than internal/aap.RunMockAdapter (which emits
// only message + turn_complete): this one also sends context, status, an
// approval_request, and a non-fatal error, so the apply* receivers in
// context.go get real frames to merge.
type fakeAAPAdapter struct {
	in       *bufio.Reader
	out      io.Writer
	approval bool
}

// runFakeAAPAdapter drives the fake adapter. When approval is true it emits
// an approval_request mid-turn and waits for the response before completing.
func runFakeAAPAdapter(ctx context.Context, in io.Reader, out io.Writer, approval bool) error {
	f := &fakeAAPAdapter{
		in:       bufio.NewReader(in),
		out:      out,
		approval: approval,
	}
	if err := f.handshake(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		line, err := aap.ReadLine(f.in)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		msg, perr := aap.ParseHostMessage(line)
		if perr != nil {
			continue
		}
		if stop, err := f.handle(msg); err != nil {
			return err
		} else if stop {
			return nil
		}
	}
}

func (f *fakeAAPAdapter) handshake() error {
	line, err := aap.ReadLine(f.in)
	if err != nil {
		return err
	}
	msg, err := aap.ParseHostMessage(line)
	if err != nil {
		return err
	}
	init, ok := msg.(aap.Initialize)
	if !ok {
		return fmt.Errorf("fake: expected initialize, got %s", msg.Type())
	}
	if init.ProtocolVersion != aap.ProtocolVersion {
		return fmt.Errorf("fake: protocol version mismatch %d", init.ProtocolVersion)
	}
	return aap.WriteMessage(f.out, aap.Ready{
		ProtocolVersion: aap.ProtocolVersion,
		Agent:           aap.AgentInfo{Name: "fake"},
		Capabilities: []string{
			aap.CapStreaming, aap.CapCancel, aap.CapContextClear,
			aap.CapExecutionContext, aap.CapToolApproval,
		},
	})
}

func (f *fakeAAPAdapter) handle(msg aap.HostMessage) (stop bool, err error) {
	switch m := msg.(type) {
	case aap.Prompt:
		return false, f.answer(m)
	case aap.ClearContext:
		return false, aap.WriteMessage(f.out, aap.Log{Level: aap.LogInfo, Message: "context cleared"})
	case aap.ApprovalResponse:
		// Acknowledge by clearing the pending approval context frame.
		return false, aap.WriteMessage(f.out, aap.ContextUpdate{Blocked: boolPtr(false), BlockedReason: strPtrClear("")})
	case aap.Shutdown:
		return true, nil
	default:
		return false, nil
	}
}

func (f *fakeAAPAdapter) answer(p aap.Prompt) error {
	if err := aap.WriteMessage(f.out, aap.Status{State: aap.StateBusy}); err != nil {
		return err
	}
	if err := aap.WriteMessage(f.out, aap.ContextUpdate{
		TurnID:       &p.TurnID,
		Note:         strPtrClear("scripted turn"),
		WaitingModel: boolPtr(true),
	}); err != nil {
		return err
	}
	reply := "fake: " + p.Content.AsText()
	if err := aap.WriteMessage(f.out, aap.Message{
		TurnID:  p.TurnID,
		Content: []aap.ContentBlock{aap.TextBlock(reply)},
	}); err != nil {
		return err
	}
	if f.approval {
		// Ask for approval, then wait for the response before completing.
		reqID := "req-" + p.TurnID
		if err := aap.WriteMessage(f.out, aap.ApprovalRequest{
			RequestID: reqID,
			ToolName:  "Bash",
		}); err != nil {
			return err
		}
		if err := aap.WriteMessage(f.out, aap.ContextUpdate{
			Blocked:       boolPtr(true),
			BlockedReason: strPtrClear("awaiting tool approval"),
		}); err != nil {
			return err
		}
		// Wait for the host's approval_response. The reader loop's handle
		// paths the response back to us via f.in.
		if err := f.waitForApproval(reqID); err != nil {
			return err
		}
	}
	if err := aap.WriteMessage(f.out, aap.TurnComplete{
		TurnID:     p.TurnID,
		StopReason: strPtrClear("end_turn"),
		ResultText: &reply,
	}); err != nil {
		return err
	}
	return aap.WriteMessage(f.out, aap.Status{State: aap.StateIdle})
}

// waitForApproval reads frames until the matching approval_response arrives.
// Other host frames (a stray cancel) are ignored.
func (f *fakeAAPAdapter) waitForApproval(reqID string) error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		line, err := aap.ReadLine(f.in)
		if err != nil {
			return err
		}
		msg, perr := aap.ParseHostMessage(line)
		if perr != nil {
			continue
		}
		if resp, ok := msg.(aap.ApprovalResponse); ok && resp.RequestID == reqID {
			return nil
		}
	}
	return fmt.Errorf("fake: approval response for %s timed out", reqID)
}

func boolPtr(b bool) *bool { return &b }

func strPtrClear(s string) *string {
	// Avoid clashing with the aap package's private strptr; this is local.
	t := strings.TrimSpace(s)
	return &t
}
