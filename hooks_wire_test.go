package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NotebookEdit spells its target notebook_path, not file_path. The wire type
// must decode it: a payload that decoded to empty paths let notebook edits
// sail past every path rule in consumers like ltk.
func TestDecodeHookPayloadNotebookEdit(t *testing.T) {
	payload := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "NotebookEdit",
		"tool_input": {"notebook_path": "/proj/analysis.ipynb", "new_source": "print(1)"}
	}`)
	p, err := DecodeHookPayload(payload)
	require.NoError(t, err)
	assert.Equal(t, "NotebookEdit", p.ToolName)
	assert.Equal(t, "/proj/analysis.ipynb", p.ToolInput.NotebookPath)
	assert.Empty(t, p.ToolInput.FilePath, "NotebookEdit carries no file_path")
}

// The file-editing tools keep their file_path spelling alongside the new
// notebook_path field.
func TestDecodeHookPayloadEdit(t *testing.T) {
	payload := []byte(`{
		"hook_event_name": "PreToolUse",
		"tool_name": "Edit",
		"tool_input": {"file_path": "/proj/main.go", "old_string": "a", "new_string": "b"}
	}`)
	p, err := DecodeHookPayload(payload)
	require.NoError(t, err)
	assert.Equal(t, "/proj/main.go", p.ToolInput.FilePath)
	assert.Empty(t, p.ToolInput.NotebookPath)
}
