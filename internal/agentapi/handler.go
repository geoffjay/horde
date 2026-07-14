// Package agentapi implements the HTTP API served by each agent subprocess
// over a unix domain socket. It exposes two endpoints:
//
//	GET  /health   — liveness check
//	POST /invoke   — run the agent, streaming events over SSE
//
// The /invoke handler drives a runner.Runner (built over the agent and an
// in-memory session service) and streams session.Event-shaped payloads as
// SSE events. Each event carries a sequential id in the SSE id: field so
// that a client reconnecting with Last-Event-ID can resume from the buffer.
//
// See docs/knowledgebase/decisions/agent-invocation-transport.md for the
// transport decision and docs/knowledgebase/plans/phase-3-agents.md for the
// full plan.
package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
)

// bufferCap is the maximum number of events retained per invocation for
// Last-Event-ID resume.
const bufferCap = 256

// invocationIDKey is the JSON key for invocation ids in SSE event payloads.
const invocationIDKey = "invocation_id"

// Handler holds the dependencies for the agent subprocess HTTP API.
type Handler struct {
	r     *runner.Runner
	regs  *invocationRegistry
	agent string
}

// NewHandler creates a Handler for the given runner and agent name.
func NewHandler(r *runner.Runner, agentName string) *Handler {
	return &Handler{
		r:     r,
		regs:  newInvocationRegistry(),
		agent: agentName,
	}
}

// Router returns the chi router for the agent subprocess HTTP API.
func (h *Handler) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/health", h.getHealth)
	r.Post("/invoke", h.invoke)
	return r
}

// healthResponse is the GET /health response.
type healthResponse struct {
	Status string `json:"status"`
}

func (h *Handler) getHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

// invokeRequest is the body of POST /invoke.
type invokeRequest struct {
	Message      string `json:"message"`
	InvocationID string `json:"invocation_id"`
	SessionID    string `json:"session_id"`
}

func (h *Handler) invoke(w http.ResponseWriter, r *http.Request) {
	var req invokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	invID := req.InvocationID
	if invID == "" {
		invID = uuid.NewString()
	}

	// sessionID determines conversation continuity across invocations. When
	// the node injects a session_id (derived from agent_id:project_id), the
	// agent retains a private conversation history per project. When empty
	// (no active project), fall back to using the invocation id, yielding a
	// fresh session per invoke.
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = invID
	}

	inv, created := h.regs.getOrCreate(invID)

	// If this is a new invocation, start the agent run in a background
	// goroutine whose lifetime is tied to the invocation, not the HTTP
	// request. This decouples the run from client disconnects so that
	// Last-Event-ID resume works: a reconnecting client attaches as a
	// new reader and replays from the buffer.
	if created {
		// Use a detached context so the run survives HTTP disconnect.
		runCtx := httpBackgroundContext()
		go h.runInvocation(runCtx, invID, sessionID, inv, req.Message)
	}

	h.streamFromBuffer(w, r, inv, parseLastEventID(r))
}

// runInvocation runs the agent in a background goroutine, appending every
// event to the invocation's ring buffer. The invocation is marked finished
// when the run completes (normally or with error). sessionID is the key for
// conversation continuity: a stable value (agent_id:project_id) retains
// history across invocations; the invocation id yields a fresh session each
// time (the fallback when there is no active project).
func (h *Handler) runInvocation(ctx context.Context, invID, sessionID string, inv *invocation, message string) {
	defer inv.markFinished()

	// Write the invocation announcement event.
	invData, _ := json.Marshal(map[string]string{
		invocationIDKey: invID,
		"agent":         h.agent,
	})
	inv.add("invocation", invData)

	msg := &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: message}},
	}

	events := h.r.Run(ctx, "local", sessionID, msg, agent.RunConfig{
		StreamingMode: agent.StreamingModeSSE,
	})

	for ev, err := range events {
		if err != nil {
			errData, _ := json.Marshal(map[string]string{
				invocationIDKey: invID,
				"error":         err.Error(),
			})
			inv.add("error", errData)
			return
		}
		if ev == nil {
			continue
		}
		data, mErr := json.Marshal(ev)
		if mErr != nil {
			continue
		}
		inv.add("token", data)
	}

	doneData, _ := json.Marshal(map[string]string{
		invocationIDKey: invID,
	})
	inv.add("done", doneData)
}

// streamFromBuffer replays buffered events with ids greater than lastID,
// then tails new events until the invocation finishes or the client
// disconnects.
func (h *Handler) streamFromBuffer(w http.ResponseWriter, r *http.Request, inv *invocation, lastID int) {
	flusher, _ := w.(http.Flusher)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Register as a subscriber to receive new-event notifications.
	sub := inv.subscribe()
	defer inv.unsubscribe(sub)

	// Replay buffered events with id > lastID.
	for _, ev := range inv.eventsAfter(lastID) {
		writeSSEEvent(w, flusher, ev.id, ev.typ, ev.data)
		lastID = ev.id
	}

	// If the invocation is already finished, we're done.
	if inv.isFinished() {
		return
	}

	// Tail new events.
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.ch:
		case <-inv.done:
		}

		// Check if the run is finished before draining. The done event
		// is added before markFinished, so if finished is true the drain
		// will include it. If finished is false, drain new events and
		// continue.
		finished := inv.isFinished()

		// Drain any new buffered events.
		for _, ev := range inv.eventsAfter(lastID) {
			writeSSEEvent(w, flusher, ev.id, ev.typ, ev.data)
			lastID = ev.id
		}

		if finished {
			// Final flush to ensure the client receives the last
			// events before the response body closes.
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
	}
}

// writeSSEEvent writes one SSE event with an id field and flushes.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, id int, eventType string, data []byte) {
	fmt.Fprintf(w, "id: %d\n", id)
	fmt.Fprintf(w, "event: %s\n", eventType)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher != nil {
		flusher.Flush()
	}
}

// parseLastEventID extracts the Last-Event-ID header as an integer.
// Returns 0 if absent or unparseable.
func parseLastEventID(r *http.Request) int {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		return 0
	}
	id, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return id
}

// writeJSON encodes v as JSON to w with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// errorResponse is a generic error envelope.
type errorResponse struct {
	Error string `json:"error"`
}
