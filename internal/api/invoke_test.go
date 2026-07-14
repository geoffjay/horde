package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteInvokeBody_WithSessionID(t *testing.T) {
	original := invokeRequestBody{Message: "hello", InvocationID: "inv-1"}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(data))
	body, err := rewriteInvokeBody(r, "a1:proj-1")
	require.NoError(t, err)

	buf := make([]byte, body.Len())
	_, _ = body.Read(buf)
	var result invokeRequestBody
	require.NoError(t, json.Unmarshal(buf, &result))
	assert.Equal(t, "hello", result.Message)
	assert.Equal(t, "inv-1", result.InvocationID)
	assert.Equal(t, "a1:proj-1", result.SessionID)
}

func TestRewriteInvokeBody_NoSessionID(t *testing.T) {
	original := invokeRequestBody{Message: "hello", InvocationID: "inv-1"}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	r := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader(data))
	body, err := rewriteInvokeBody(r, "")
	require.NoError(t, err)

	buf := make([]byte, body.Len())
	_, _ = body.Read(buf)
	var result invokeRequestBody
	require.NoError(t, json.Unmarshal(buf, &result))
	assert.Equal(t, "hello", result.Message)
	assert.Equal(t, "inv-1", result.InvocationID)
	assert.Empty(t, result.SessionID)
}

func TestRewriteInvokeBody_InvalidJSON(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/invoke", bytes.NewReader([]byte("not json")))
	_, err := rewriteInvokeBody(r, "a1:proj-1")
	assert.Error(t, err)
}

func TestRewriteInvokeBody_EmptyBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/invoke", nil)
	body, err := rewriteInvokeBody(r, "a1:proj-1")
	require.NoError(t, err)

	buf := make([]byte, body.Len())
	_, _ = body.Read(buf)
	var result invokeRequestBody
	require.NoError(t, json.Unmarshal(buf, &result))
	assert.Empty(t, result.Message)
	assert.Equal(t, "a1:proj-1", result.SessionID)
}
