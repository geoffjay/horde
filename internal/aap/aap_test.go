package aap

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vectorFile mirrors testdata/vectors.json.
type vectorFile struct {
	ProtocolVersion int `json:"protocol_version"`
	Cases           []struct {
		Name      string          `json:"name"`
		Direction string          `json:"direction"`
		Message   json.RawMessage `json:"message"`
	} `json:"cases"`
}

// TestVectors round-trips every canonical vector: parse it into a typed
// message, re-marshal, and assert the output is JSON-equivalent to the input.
// This validates both decoding and encoding against the shared wire form.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	require.NoError(t, err)

	var vf vectorFile
	require.NoError(t, json.Unmarshal(raw, &vf))
	require.Equal(t, ProtocolVersion, vf.ProtocolVersion)
	require.NotEmpty(t, vf.Cases)

	for _, c := range vf.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var got []byte
			switch c.Direction {
			case "host":
				m, err := ParseHostMessage(c.Message)
				require.NoError(t, err)
				got, err = json.Marshal(m)
				require.NoError(t, err)
			case "agent":
				m, err := ParseAgentMessage(c.Message)
				require.NoError(t, err)
				got, err = json.Marshal(m)
				require.NoError(t, err)
			default:
				t.Fatalf("unknown direction %q", c.Direction)
			}
			assert.JSONEq(t, string(c.Message), string(got))
		})
	}
}

func TestParseHostMessage_UnknownType(t *testing.T) {
	_, err := ParseHostMessage([]byte(`{"type":"nope"}`))
	require.Error(t, err)
	var unknown *UnknownTypeError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, "nope", unknown.Type)
}

// A frame from the wrong direction (an agent message parsed as a host message)
// is reported as an unknown type, not a decode error.
func TestParseHostMessage_WrongDirection(t *testing.T) {
	_, err := ParseHostMessage([]byte(`{"type":"ready","protocol_version":1,"agent":{"name":"x"},"capabilities":[]}`))
	var unknown *UnknownTypeError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, TypeReady, unknown.Type)
}

func TestPeekType_Missing(t *testing.T) {
	_, err := ParseAgentMessage([]byte(`{"state":"busy"}`))
	require.Error(t, err)
	var unknown *UnknownTypeError
	assert.NotErrorAs(t, err, &unknown, "a missing type is a malformed frame, not an unknown type")
}

// Forward compatibility: an unknown field on a known message must not break
// parsing.
func TestUnknownFieldsIgnored(t *testing.T) {
	m, err := ParseAgentMessage([]byte(`{"type":"status","state":"busy","future_field":123}`))
	require.NoError(t, err)
	status, ok := m.(Status)
	require.True(t, ok)
	assert.Equal(t, StateBusy, status.State)
}

// Explicit null optionals parse into nil and are omitted on re-encode, so the
// canonical form drops them.
func TestNullOptionalsParseAndOmit(t *testing.T) {
	in := `{"type":"initialize","protocol_version":1,"model":null,` +
		`"system_prompt":{"mode":"replace","text":"hi","path":null},` +
		`"workspace":{"cwd":"/repo"},"tools":null,"permissions":null,"resume_token":null}`
	m, err := ParseHostMessage([]byte(in))
	require.NoError(t, err)
	init, ok := m.(Initialize)
	require.True(t, ok)
	assert.Nil(t, init.Model)
	assert.Nil(t, init.ResumeToken)
	require.NotNil(t, init.SystemPrompt)
	assert.Nil(t, init.SystemPrompt.Path)

	out, err := json.Marshal(init)
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"type":"initialize","protocol_version":1,"system_prompt":{"mode":"replace","text":"hi"},"workspace":{"cwd":"/repo"}}`,
		string(out))
}

func TestPromptContent_AsText(t *testing.T) {
	assert.Equal(t, "hi", TextPrompt("hi").AsText())

	blocks := BlockPrompt(TextBlock("a"), ThinkingBlock("z"), TextBlock("b"))
	assert.Equal(t, "ab", blocks.AsText(), "thinking blocks are skipped")
	assert.False(t, blocks.IsText())
	assert.Len(t, blocks.Blocks(), 3)
}

func TestTransportFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	assert.Equal(t, TransportStdio, TransportFromEnv(env(nil)), "default is stdio")
	assert.Equal(t, TransportWebSocket,
		TransportFromEnv(env(map[string]string{LegacyEnvTransport: TransportWebSocket})),
		"legacy alias is honored")
	assert.Equal(t, TransportStdio,
		TransportFromEnv(env(map[string]string{
			EnvTransport:       TransportStdio,
			LegacyEnvTransport: TransportWebSocket,
		})),
		"canonical takes precedence over legacy")

	assert.Equal(t, "ws://x/y",
		WSURLFromEnv(env(map[string]string{LegacyEnvWSURL: "ws://x/y"})))
}
