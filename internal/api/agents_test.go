package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/aap"
	"github.com/geoffjay/horde/internal/server"
)

// stubAgentView implements agentView for approval-handler mapping tests. The
// embedded nil interface satisfies the methods the handler does not call
// (calling one would panic — the intended signal in a focused test).
type stubAgentView struct {
	agentView
	respondErr error
	ctx        *server.ExecutionContext
}

func (s stubAgentView) RespondApproval(_, _ string, _ aap.ApprovalDecision) error {
	return s.respondErr
}
func (s stubAgentView) AgentContext(string) *server.ExecutionContext { return s.ctx }
func (s stubAgentView) ContextShareFull() bool                       { return true }

// approvalHandler mounts respondApproval on a minimal chi router so URL params
// resolve, without needing a real server or subprocess.
func approvalHandler(v agentView) http.Handler {
	r := chi.NewRouter()
	r.Post("/api/v1/agents/{id}/approvals/{requestID}", respondApproval(v))
	return r
}

func TestRespondApproval_BadDecision(t *testing.T) {
	h := approvalHandler(stubAgentView{})
	w := do(t, h, http.MethodPost, "/api/v1/agents/a1/approvals/r1", map[string]string{"decision": "maybe"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRespondApproval_UnknownAgent(t *testing.T) {
	h := approvalHandler(stubAgentView{respondErr: server.ErrAgentNotFound})
	w := do(t, h, http.MethodPost, "/api/v1/agents/a1/approvals/r1", map[string]string{"decision": "allow"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRespondApproval_UnknownRequest(t *testing.T) {
	h := approvalHandler(stubAgentView{respondErr: server.ErrApprovalNotFound})
	w := do(t, h, http.MethodPost, "/api/v1/agents/a1/approvals/r1", map[string]string{"decision": "deny"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRespondApproval_NotAAP(t *testing.T) {
	h := approvalHandler(stubAgentView{respondErr: server.ErrNotAAPAgent})
	w := do(t, h, http.MethodPost, "/api/v1/agents/a1/approvals/r1", map[string]string{"decision": "allow"})
	assert.Equal(t, http.StatusConflict, w.Code)
}

func TestRespondApproval_Allows(t *testing.T) {
	h := approvalHandler(stubAgentView{ctx: &server.ExecutionContext{AgentID: "a1"}})
	w := do(t, h, http.MethodPost, "/api/v1/agents/a1/approvals/r1", map[string]string{"decision": "allow"})
	require.Equal(t, http.StatusOK, w.Code)

	var ec server.ExecutionContext
	require.NoError(t, json.NewDecoder(w.Body).Decode(&ec))
	assert.Equal(t, "a1", ec.AgentID)
}

// TestRespondApproval_RouteWired confirms the route is registered on the real
// router and maps an unknown agent to 404 through the full stack.
func TestRespondApproval_RouteWired(t *testing.T) {
	srv := newTestServer(t)
	w := do(t, Router(srv), http.MethodPost, "/api/v1/agents/nope/approvals/r1",
		map[string]string{"decision": "allow"})
	assert.Equal(t, http.StatusNotFound, w.Code)
}
