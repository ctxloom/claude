// Tests for ClaudeSessionHistory.TranscriptPathFromHook (the path-derivation
// surface). Previous-session resolution is covered by
// TestClaudeSessionHistory_GetPreviousSession_ReadTime in claude_session_test.go.
package claude

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// TranscriptPathFromHook
// =============================================================================

func TestClaudeSessionHistory_TranscriptPathFromHook_BuildsExpectedPath(t *testing.T) {
	h := NewClaudeSessionHistory(NewClaudeCode(writeClaudeSettings),
		WithClaudeSessionHomeDir("/synthetic/home"),
	)

	// Claude's per-project transcript dir replaces path separators with
	// dashes: /test/project → -test-project. Pin that here so a future
	// refactor that changes the convention shows up loudly.
	got := h.TranscriptPathFromHook("/test/project", "sess-abc", "ignored")
	want := filepath.Join("/synthetic/home", ".claude", "projects", "-test-project", "sess-abc.jsonl")
	assert.Equal(t, want, got)
}

func TestClaudeSessionHistory_TranscriptPathFromHook_EmptySessionIDReturnsEmpty(t *testing.T) {
	h := NewClaudeSessionHistory(NewClaudeCode(writeClaudeSettings),
		WithClaudeSessionHomeDir("/synthetic/home"),
	)
	assert.Empty(t, h.TranscriptPathFromHook("/p", "", "x"),
		"empty session id is a structural absence, not a guess at the path")
}
