package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/geoffjay/horde/internal/server"
)

// streamEvents streams cluster-activity events (agent lifecycle transitions)
// over SSE. On the master the feed is cluster-wide: slaves forward their
// events to the master (POST /cluster/events), which republishes them here.
// The stream carries only live events — there is no backlog replay, so
// Last-Event-ID resume is not offered (the id is for client correlation only).
func streamEvents(srv eventView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		events, cancel := srv.SubscribeEvents()
		defer cancel()

		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		// Flush the headers immediately so the client's connection is
		// established as soon as it subscribes, rather than blocking until the
		// first event happens to arrive. Without this, a client that opens the
		// stream and then triggers activity would deadlock (the trigger waits
		// on the open, the open waits on an event).
		if flusher != nil {
			flusher.Flush()
		}

		var id int
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				id++
				writeEventSSE(w, flusher, id, ev)
			}
		}
	}
}

// receiveClusterEvent accepts an event forwarded by a slave and republishes it
// onto this node's bus, making the master's /events/stream cluster-wide. It is
// master-only: a slave (which forwards its own events upward) rejects it.
func receiveClusterEvent(srv eventView) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.Mode() != server.ModeMaster {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "cluster events are accepted by the master only"})
			return
		}
		var ev server.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: errInvalidBody})
			return
		}
		if ev.Type == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "type is required"})
			return
		}
		srv.PublishClusterEvent(ev)
		writeJSON(w, http.StatusOK, clusterEventResponse{OK: true})
	}
}

// clusterEventResponse is the POST /api/v1/cluster/events response.
type clusterEventResponse struct {
	OK bool `json:"ok"`
}

// writeEventSSE writes one SSE event frame (an incrementing id, the event
// type, and the JSON payload) and flushes.
func writeEventSSE(w http.ResponseWriter, flusher http.Flusher, id int, ev server.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: ", id, ev.Type); err != nil {
		return
	}
	if _, err := w.Write(data); err != nil {
		return
	}
	if _, err := io.WriteString(w, "\n\n"); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}
