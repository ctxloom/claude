package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "streamjson", name))
	require.NoError(t, err)
	return b
}

func TestMapStreamJSONEvent_AssistantText_OneAssistantEntry(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "assistant_text.json"))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[0].Entry.Type)
	assert.Equal(t, "hello-from-tool", evs[0].Entry.Content)
}

func TestMapStreamJSONEvent_AssistantToolUse_OneToolUseEntry(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "assistant_tooluse.json"))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeToolUse, evs[0].Entry.Type)
	assert.Equal(t, "Bash", evs[0].Entry.ToolName)
	assert.Contains(t, string(evs[0].Entry.ToolInput), "echo hello-from-tool")
}

func TestMapStreamJSONEvent_ToolResult_OneToolResultEntry(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "user_toolresult.json"))
	require.Len(t, evs, 1)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeToolResult, evs[0].Entry.Type)
	assert.Equal(t, "hello-from-tool", evs[0].Entry.ToolOutput)
	assert.False(t, evs[0].Entry.IsError)
}

func TestMapStreamJSONEvent_Result_CompleteWithMetadata(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "result_success.json"))
	require.Len(t, evs, 1)
	c := evs[0].Complete
	require.NotNil(t, c)
	assert.Equal(t, "claude-opus-4-8", c.Model)
	assert.Equal(t, 1_000_000, c.ContextWindow)
	assert.Equal(t, 64_000, c.MaxOutputTokens)
	assert.Greater(t, c.InputTokens, 0)
	assert.Greater(t, c.CacheReadTokens, 0)
	assert.InDelta(t, 0.1667485, c.CostUSD, 1e-6)
	assert.Equal(t, "end_turn", c.StopReason)
	assert.Equal(t, 1, c.NumTurns)
	assert.Nil(t, evs[0].Entry, "result is metadata only — no content (no double-render of result string)")
}

func TestMapStreamJSONEvent_SystemInit_SessionInfo(t *testing.T) {
	evs := mapStreamJSONEvent(fixture(t, "system_init.json"))
	require.Len(t, evs, 1)
	s := evs[0].Session
	require.NotNil(t, s)
	assert.Equal(t, "claude-sonnet-4-6", s.Model)
	assert.Equal(t, "bypassPermissions", s.PermissionMode)
	require.NotEmpty(t, s.MCPServers)
	assert.Equal(t, "spotify", s.MCPServers[0].Name)
	assert.Equal(t, "connected", s.MCPServers[0].Status)
}

func TestMapStreamJSONEvent_RateLimit_Dropped(t *testing.T) {
	assert.Empty(t, mapStreamJSONEvent(fixture(t, "rate_limit_event.json")))
}

// A new/unknown event type, malformed JSON, and the many dropped system subtypes
// must all yield nothing rather than crash the stream.
func TestMapStreamJSONEvent_UnknownOrNoise_Dropped(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"type":"totally_new_event_type"}`),
		[]byte(`{"type":"system","subtype":"hook_started"}`),
		[]byte(`{"type":"system","subtype":"thinking_tokens"}`),
		[]byte(`{"type":"system","subtype":"commands_changed"}`),
		[]byte(`not even json`),
		[]byte(``),
	} {
		assert.Empty(t, mapStreamJSONEvent(raw), "should drop: %s", raw)
	}
}

// One assistant event with multiple content blocks expands to one Entry per
// renderable block, in order; thinking blocks are dropped.
func TestMapStreamJSONEvent_MultiBlockAssistant_ExpandsInOrder(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"content":[
		{"type":"thinking","thinking":"hmm"},
		{"type":"tool_use","name":"Read","input":{"path":"x"}},
		{"type":"text","text":"done"}]}}`)
	evs := mapStreamJSONEvent(raw)
	require.Len(t, evs, 2)
	require.NotNil(t, evs[0].Entry)
	assert.Equal(t, agent.EntryTypeToolUse, evs[0].Entry.Type)
	assert.Equal(t, "Read", evs[0].Entry.ToolName)
	require.NotNil(t, evs[1].Entry)
	assert.Equal(t, agent.EntryTypeAssistant, evs[1].Entry.Type)
	assert.Equal(t, "done", evs[1].Entry.Content)
}
