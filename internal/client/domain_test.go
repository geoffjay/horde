package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- projects ---

func projectsHandler(t *testing.T, stub *projectsStub) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		stub.serve(w, r)
	})
	return mux
}

type projectsStub struct {
	projects []Project
}

func (s *projectsStub) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		if r.URL.Path == "/api/v1/projects/" {
			_ = json.NewEncoder(w).Encode(s.projects)
			return
		}
		id := r.URL.Path[len("/api/v1/projects/"):]
		for _, p := range s.projects {
			if p.ID == id {
				_ = json.NewEncoder(w).Encode(p)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		return
	case http.MethodPost:
		var req CreateProjectRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		p := Project{ID: "p1", Name: req.Name, Workspace: req.Workspace, Goal: req.Goal, State: "active"}
		s.projects = append(s.projects, p)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(p)
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestListProjects(t *testing.T) {
	stub := &projectsStub{projects: []Project{
		{ID: "p1", Name: "auth-service", State: "active"},
		{ID: "p2", Name: "billing", State: "paused"},
	}}
	srv := httptest.NewServer(projectsHandler(t, stub))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ps, err := c.ListProjects(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, ps, 2)
	assert.Equal(t, "auth-service", ps[0].Name)
}

func TestGetProject(t *testing.T) {
	stub := &projectsStub{projects: []Project{{ID: "p1", Name: "auth", State: "active"}}}
	srv := httptest.NewServer(projectsHandler(t, stub))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	p, err := c.GetProject(context.Background(), "p1")
	require.NoError(t, err)
	assert.Equal(t, "auth", p.Name)

	_, err = c.GetProject(context.Background(), "missing")
	require.Error(t, err)
}

func TestCreateProject(t *testing.T) {
	stub := &projectsStub{}
	srv := httptest.NewServer(projectsHandler(t, stub))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	p, err := c.CreateProject(context.Background(), CreateProjectRequest{
		Name: "auth-service", Workspace: "~/work/auth", Goal: "Fix login", AgentNames: []string{"greeter"},
	})
	require.NoError(t, err)
	assert.Equal(t, "auth-service", p.Name)
	assert.Equal(t, "active", p.State)
}

func TestProjectLifecycle(t *testing.T) {
	stub := &projectsStub{projects: []Project{{ID: "p1", Name: "auth", State: "active"}}}
	srv := httptest.NewServer(lifecycleHandler(t, &stub.projects))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())

	p, err := c.PauseProject(context.Background(), "p1")
	require.NoError(t, err)
	assert.Equal(t, "paused", p.State)

	p, err = c.ResumeProject(context.Background(), "p1")
	require.NoError(t, err)
	assert.Equal(t, "active", p.State)

	p, err = c.FinishProject(context.Background(), "p1")
	require.NoError(t, err)
	assert.Equal(t, "finished", p.State)
}

func lifecycleHandler(t *testing.T, projects *[]Project) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch r.Method {
		case http.MethodPost:
			switch {
			case hasSuffix(path, "/pause"):
				setState(projects, "paused")
			case hasSuffix(path, "/resume"):
				setState(projects, "active")
			case hasSuffix(path, "/finish"):
				setState(projects, "finished")
			case hasSuffix(path, "/agents"):
				var req map[string]string
				_ = json.NewDecoder(r.Body).Decode(&req)
				p := &(*projects)[0]
				p.Team.Agents = append(p.Team.Agents, TeamAgent{AgentID: "a1", Name: req["name"]})
				_ = json.NewEncoder(w).Encode(*p)
				return
			}
		case http.MethodDelete:
			if hasSuffix(path, "/agents/a1") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode((*projects)[0])
	})
	return mux
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func setState(projects *[]Project, state string) {
	for i := range *projects {
		(*projects)[i].State = state
	}
}

func TestAssignAndRemoveAgent(t *testing.T) {
	stub := &projectsStub{projects: []Project{{ID: "p1", Name: "auth", State: "active"}}}
	srv := httptest.NewServer(lifecycleHandler(t, &stub.projects))
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	p, err := c.AssignAgent(context.Background(), "p1", "coder")
	require.NoError(t, err)
	require.Len(t, p.Team.Agents, 1)
	assert.Equal(t, "coder", p.Team.Agents[0].Name)

	require.NoError(t, c.RemoveAgent(context.Background(), "p1", "a1"))
}

// --- execution context ---

func TestListAgentContexts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/context", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]ExecutionContext{
			{AgentID: "a1", Activity: StateBusy, Lifecycle: AgentRunning},
			{AgentID: "a2", Activity: StateIdle, Lifecycle: AgentRunning},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctxs, err := c.ListAgentContexts(context.Background())
	require.NoError(t, err)
	require.Len(t, ctxs, 2)
	assert.Equal(t, StateBusy, ctxs[0].Activity)
}

func TestGetAgentContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/a1/context", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ExecutionContext{AgentID: "a1", Lifecycle: AgentRunning})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, err := c.GetAgentContext(context.Background(), "a1")
	require.NoError(t, err)
	assert.Equal(t, "a1", ctx.AgentID)
	assert.Equal(t, AgentRunning, ctx.Lifecycle)
}

func TestStreamAgentContext(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/a1/context/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		writeSSE(w, flusher, "context", ExecutionContext{AgentID: "a1", Activity: StateBusy})
		writeSSE(w, flusher, "context", ExecutionContext{AgentID: "a1", Activity: StateIdle})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := c.StreamAgentContext(ctx, "a1")
	require.NoError(t, err)

	var got []ActivityState
	for ec := range ch {
		got = append(got, ec.Activity)
		if len(got) == 2 {
			break
		}
	}
	assert.Equal(t, []ActivityState{StateBusy, StateIdle}, got)
}

// --- cluster ---

func TestListNodes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cluster/nodes", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ClusterView{
			LeaderID: "n1",
			Nodes: []ClusterNode{
				{NodeID: "n1", Addr: "127.0.0.1:8080", Agents: []string{"a1"}, LastSeen: "2026-07-14T00:00:00Z"},
				{NodeID: "n2", Addr: "10.0.0.12:8080", Agents: []string{"a2"}, Stale: true},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	view, err := c.ListNodes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "n1", view.LeaderID)
	require.Len(t, view.Nodes, 2)
	assert.False(t, view.Nodes[0].Stale)
	assert.True(t, view.Nodes[1].Stale)
	assert.Equal(t, time.Time{}, view.Nodes[1].ParseLastSeen(), "empty last_seen -> zero")
}

func TestListRemoteAgentContexts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/cluster/agents/context", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("issue") == "#142" {
			_ = json.NewEncoder(w).Encode([]ExecutionContext{{AgentID: "a2", Issue: "#142"}})
			return
		}
		_ = json.NewEncoder(w).Encode([]ExecutionContext{{AgentID: "a2"}, {AgentID: "a3"}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctxs, err := c.ListRemoteAgentContexts(context.Background(), "#142")
	require.NoError(t, err)
	require.Len(t, ctxs, 1)
	assert.Equal(t, "#142", ctxs[0].Issue)

	all, err := c.ListRemoteAgentContexts(context.Background(), "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// --- invoke ---

func TestInvoke(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/a1/invoke", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		writeSSE(w, flusher, "invocation", InvokeResult{InvocationID: "inv-1"})
		writeSSE(w, flusher, "token", InvokeToken{Text: "hello"})
		writeSSE(w, flusher, "token", InvokeToken{Text: " world"})
		writeSSE(w, flusher, "done", InvokeDone{InvocationID: "inv-1"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := c.Invoke(ctx, "a1", InvokeRequest{Message: "hi"})
	require.NoError(t, err)

	var events []InvokeEvent
	for ev := range ch {
		events = append(events, ev)
		if ev.Type == "done" {
			break
		}
	}
	require.Len(t, events, 4)
	assert.Equal(t, "invocation", events[0].Type)
	assert.Equal(t, "token", events[1].Type)
	assert.Equal(t, "done", events[3].Type)

	var tok InvokeToken
	require.NoError(t, json.Unmarshal(events[1].Data, &tok))
	assert.Equal(t, "hello", tok.Text)
}

func TestInvoke_409Paused(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agents/a1/invoke", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "project is paused"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.Listener.Addr().String())
	_, err := c.Invoke(context.Background(), "a1", InvokeRequest{Message: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "409")
}

// writeSSE writes one SSE event and flushes.
func writeSSE(w io.Writer, flusher http.Flusher, eventType string, v any) {
	data, _ := json.Marshal(v)
	_, _ = w.Write([]byte("event: " + eventType + "\n"))
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(data)
	_, _ = w.Write([]byte("\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}
