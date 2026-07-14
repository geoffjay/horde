package app

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/geoffjay/horde/internal/client"
)

func TestHintStatusBlock_DisconnectedOmits(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = false

	block := hintStatusBlock()
	assert.Empty(t, block.Render(m), "hint block should be empty when disconnected")
}

func TestHintStatusBlock_ProjectsView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjects

	block := hintStatusBlock()
	hint := block.Render(m)
	assert.Contains(t, hint, "select")
	assert.Contains(t, hint, "enter open")
}

func TestHintStatusBlock_ProjectDetailActive(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjectDetail
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "active"}}
	m.cursor = 0

	block := hintStatusBlock()
	hint := block.Render(m)
	assert.Contains(t, hint, "enter invoke")
	assert.Contains(t, hint, "a assign")
	assert.Contains(t, hint, "p pause")
	assert.Contains(t, hint, "esc back")
}

func TestHintStatusBlock_ProjectDetailPaused(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjectDetail
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "paused"}}
	m.cursor = 0

	block := hintStatusBlock()
	hint := block.Render(m)
	assert.Contains(t, hint, "r resume")
	assert.NotContains(t, hint, "p pause", "paused project should not show pause hint")
}

func TestHintStatusBlock_ProjectDetailFinished(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjectDetail
	m.projects = []client.Project{{ID: "p1", Name: "auth", State: "finished"}}
	m.cursor = 0

	block := hintStatusBlock()
	hint := block.Render(m)
	assert.NotContains(t, hint, "enter invoke", "finished project should not show invoke hint")
	assert.Contains(t, hint, "a assign")
	assert.Contains(t, hint, "esc back")
}

func TestHintStatusBlock_AgentView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewAgent

	block := hintStatusBlock()
	hint := block.Render(m)
	assert.Contains(t, hint, "enter invoke")
	assert.Contains(t, hint, "esc back")
}

func TestHintStatusBlock_InvokeView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewInvoke

	block := hintStatusBlock()
	hint := block.Render(m)
	assert.Contains(t, hint, "enter send")
	assert.Contains(t, hint, "esc back")
}

func TestHintStatusBlock_ClusterView(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewCluster

	block := hintStatusBlock()
	hint := block.Render(m)
	assert.Contains(t, hint, "enter node")
	assert.Contains(t, hint, "esc back")
}

func TestDefaultStatusLine_HasThreeBlocks(t *testing.T) {
	s := DefaultStatusLine()
	require.Len(t, s.blocks, 3)
	assert.Equal(t, "node", s.blocks[0].Name)
	assert.Equal(t, "hint", s.blocks[1].Name)
	assert.Equal(t, "commands", s.blocks[2].Name)
}

func TestDefaultStatusLine_RenderContainsHint(t *testing.T) {
	stub := httptest.NewServer(navTestHandler())
	defer stub.Close()

	m := New(context.Background(), stub.Listener.Addr().String())
	m.Update(m.loadNode())
	m.connected = true

	out := m.status.Render(m, 100)
	assert.Contains(t, out, "select", "status line should contain the hint text")
	assert.Contains(t, out, "enter open")
}

func TestStatusLine_HintDimsWithPalette(t *testing.T) {
	m := New(context.Background(), "127.0.0.1:1")
	m.connected = true
	m.view = viewProjects

	// Hint renders normally when palette is closed.
	hint := hintStatusBlock().Render(m)
	assert.NotEmpty(t, hint)

	// When palette is open, paint returns unstyled text (dimming is applied
	// by the View compositor, not by the block itself). The block still
	// produces text — it just won't carry the faint style.
	m.openPalette()
	hintOpen := hintStatusBlock().Render(m)
	assert.NotEmpty(t, hintOpen, "hint block should still produce text when palette is open")
}
