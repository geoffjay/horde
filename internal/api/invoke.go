package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/geoffjay/horde/internal/server"
)

// invokeRequest is the body of POST /api/v1/agents/{id}/invoke.
type invokeRequest struct {
	Message string `json:"message"`
}

// invokeAgent streams agent invocation events over SSE. For Phase 2 the
// agent subprocess does not yet emit structured events (see the Phase 2
// plan: "this can be stubbed"); this handler establishes the SSE contract
// and returns a single done event so the pipe is proven end-to-end. Phase 3
// replaces the done-only response with real token/log events from the bus.
func invokeAgent(_ agentView, bus *server.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")

		var req invokeRequest
		_ = json.NewDecoder(r.Body).Decode(&req) // message is optional for the stub

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		flusher, _ := w.(http.Flusher)

		invocationID := server.NewInvocationID()
		writeSSE(w, flusher, "invocation", map[string]string{"invocation_id": invocationID, "agent_id": id})

		// Subscribe to the bus for this invocation so Phase 3 can publish
		// real events; for now we publish a single done event ourselves.
		events, cancel := bus.Subscribe(invocationID)
		defer cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range events {
				writeSSE(w, flusher, ev.Type, ev.Data)
			}
		}()

		// Phase 2 stub: emit a done event immediately. Phase 3 replaces
		// this with the real agent run loop publishing token/log events.
		bus.Publish(server.Event{
			InvocationID: invocationID,
			Type:         "done",
			Data:         map[string]string{"invocation_id": invocationID},
		})

		select {
		case <-done:
		case <-r.Context().Done():
		}
	}
}

// writeSSE writes one SSE event to the response and flushes. It is
// best-effort; write errors are swallowed because the client may have
// disconnected.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		return
	}
	if _, err := w.Write([]byte("event: " + eventType + "\n")); err != nil {
		return
	}
	if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}
