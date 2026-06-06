package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeCodeHookWriter_WriteHooks(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{
				{Command: "./pre-tool.sh", Matcher: "Bash"},
			},
			PostTool: []wire.Hook{
				{Command: "./post-tool.sh", Matcher: "Edit"},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was created
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("failed to read settings.json: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("failed to parse settings.json: %v", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("expected hooks in settings")
	}

	// Check PreToolUse
	preToolUse, ok := hooks["PreToolUse"].([]interface{})
	if !ok || len(preToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse matcher, got %v", hooks["PreToolUse"])
	}

	// Check PostToolUse
	postToolUse, ok := hooks["PostToolUse"].([]interface{})
	if !ok || len(postToolUse) != 1 {
		t.Errorf("expected 1 PostToolUse matcher, got %v", hooks["PostToolUse"])
	}
}
func TestClaudeCodeHookWriter_PreservesUserHooks(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing settings with user hooks (no _ctxloom field)
	claudeDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(claudeDir, 0755)

	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "./user-hook.sh",
							// No _ctxloom field - user-defined
						},
					},
				},
			},
		},
		"otherSetting": "preserved",
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write ctxloom hooks
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{
				{Command: "./ctxloom-hook.sh", Matcher: "Bash"},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back and verify
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ = os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	// otherSetting should be preserved
	if settings["otherSetting"] != "preserved" {
		t.Error("expected otherSetting to be preserved")
	}

	// Both user hook and ctxloom hook should exist
	hooks := settings["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})

	// Should have 2 matchers (one for user, one for ctxloom) or combined
	totalHooks := 0
	for _, matcher := range preToolUse {
		m := matcher.(map[string]interface{})
		hooksList := m["hooks"].([]interface{})
		totalHooks += len(hooksList)
	}

	if totalHooks < 2 {
		t.Errorf("expected at least 2 hooks (user + ctxloom), got %d", totalHooks)
	}
}
func TestClaudeCodeHookWriter_RemovesOldScmHooks(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing settings with ctxloom hooks (identified by command pattern).
	// Note: We no longer use _ctxloom marker field since Claude Code uses strict
	// schema validation. Hooks are identified by command containing "ctxloom" AND "inject-context".
	claudeDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(claudeDir, 0755)

	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "\"/old/path/to/ctxloom\" hook inject-context --project \"/some/path\" oldhash123",
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write new ctxloom hooks
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{
				{Command: "./new-ctxloom-hook.sh", Matcher: "Edit"},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back and verify old ctxloom hook is gone
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ = os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})

	// Should only have the new ctxloom hook with Edit matcher
	for _, matcher := range preToolUse {
		m := matcher.(map[string]interface{})
		if m["matcher"] == "Bash" {
			hooksList := m["hooks"].([]interface{})
			for _, h := range hooksList {
				hook := h.(map[string]interface{})
				cmd := hook["command"].(string)
				if strings.Contains(cmd, "oldhash123") {
					t.Error("old ctxloom hook should have been removed")
				}
			}
		}
	}
}
func TestClaudeCodeHookWriter_RemovesHooksWithoutMarkerByCommand(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing settings with inject-context hooks that DON'T have _ctxloom field
	// This simulates hooks from an older version or corrupted state
	claudeDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(claudeDir, 0755)

	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						// Old inject-context hook WITHOUT _ctxloom marker
						map[string]interface{}{
							"type":    "command",
							"command": "\"/path/to/ctxloom\" hook inject-context --project \"/some/path\" abc123",
							"timeout": 60,
							// Note: NO "_ctxloom" field - this is the bug case
						},
						// Duplicate
						map[string]interface{}{
							"type":    "command",
							"command": "\"/path/to/ctxloom\" hook inject-context --project \"/some/path\" abc123",
							"timeout": 60,
						},
						// User's own hook (should be preserved)
						map[string]interface{}{
							"type":    "command",
							"command": "echo 'user hook'",
							"timeout": 30,
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write new ctxloom hooks
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{
				{Command: "\"/new/ctxloom\" hook inject-context --project \"/new/path\" newhash", Timeout: 60},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back and verify
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ = os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})

	// Count the hooks
	totalHooks := 0
	userHooks := 0
	ctxloomHooks := 0

	for _, matcher := range sessionStart {
		m := matcher.(map[string]interface{})
		hooksList := m["hooks"].([]interface{})
		for _, h := range hooksList {
			hook := h.(map[string]interface{})
			cmd := hook["command"].(string)
			totalHooks++
			if cmd == "echo 'user hook'" {
				userHooks++
			}
			if strings.Contains(cmd, "inject-context") {
				ctxloomHooks++
			}
		}
	}

	// Should have exactly 1 user hook preserved
	if userHooks != 1 {
		t.Errorf("expected 1 user hook, got %d", userHooks)
	}

	// Should have exactly 1 ctxloom hook (the new one, old duplicates removed)
	if ctxloomHooks != 1 {
		t.Errorf("expected 1 ctxloom hook (new), got %d - old hooks may not have been removed", ctxloomHooks)
	}

	// Total should be 2 (1 user + 1 new ctxloom)
	if totalHooks != 2 {
		t.Errorf("expected 2 total hooks, got %d", totalHooks)
	}
}
func TestClaudeCodeHookWriter_DedupsBundleShippedHooks(t *testing.T) {
	tmpDir := t.TempDir()

	writer := &ClaudeCodeHookWriter{}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PostFileEdit: []wire.Hook{
				{Command: "ctxloom hook stamp-plan", Type: "command"},
			},
		},
	}

	// Write hooks three times. Without the fix, each apply would append
	// a duplicate of the previous run's hooks.
	for range 3 {
		if err := writer.WriteHooks(cfg, tmpDir); err != nil {
			t.Fatalf("WriteHooks: %v", err)
		}
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// The stamp-plan hook (PostFileEdit → PostToolUse Edit|Write) must
	// appear exactly once across the whole PostToolUse tree, not once
	// per apply.
	hooks := settings["hooks"].(map[string]any)
	postToolUse := hooks["PostToolUse"].([]any)
	stampCount := 0
	for _, matcher := range postToolUse {
		m := matcher.(map[string]any)
		hooksList := m["hooks"].([]any)
		for _, h := range hooksList {
			cmd := h.(map[string]any)["command"].(string)
			if strings.Contains(cmd, "hook stamp-plan") {
				stampCount++
			}
		}
	}
	if stampCount != 1 {
		t.Errorf("expected exactly 1 `hook stamp-plan` hook after 3 applies, got %d", stampCount)
	}
}
func TestClaudeCodeHookWriter_UnifiedToBackendMapping(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreShell:     []wire.Hook{{Command: "./pre-shell.sh"}},
			PostFileEdit: []wire.Hook{{Command: "./post-edit.sh"}},
			SessionStart: []wire.Hook{{Command: "./start.sh"}},
			SessionEnd:   []wire.Hook{{Command: "./end.sh"}},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ := os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})

	// PreShell maps to PreToolUse with Bash matcher
	preToolUse := hooks["PreToolUse"].([]interface{})
	foundBashMatcher := false
	for _, m := range preToolUse {
		matcher := m.(map[string]interface{})
		if matcher["matcher"] == "Bash" {
			foundBashMatcher = true
		}
	}
	if !foundBashMatcher {
		t.Error("PreShell should map to PreToolUse with Bash matcher")
	}

	// PostFileEdit maps to PostToolUse with Edit|Write matcher
	postToolUse := hooks["PostToolUse"].([]interface{})
	foundEditMatcher := false
	for _, m := range postToolUse {
		matcher := m.(map[string]interface{})
		if matcher["matcher"] == "Edit|Write" {
			foundEditMatcher = true
		}
	}
	if !foundEditMatcher {
		t.Error("PostFileEdit should map to PostToolUse with Edit|Write matcher")
	}

	// SessionStart and SessionEnd should be present
	if _, ok := hooks["SessionStart"]; !ok {
		t.Error("expected SessionStart hook")
	}
	if _, ok := hooks["SessionEnd"]; !ok {
		t.Error("expected SessionEnd hook")
	}
}
func TestClaudeCodeHookWriter_BackendPassthrough(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	cfg := &wire.HooksConfig{
		Plugins: map[string]wire.BackendHooks{
			"claude-code": {
				"Notification": []wire.Hook{
					{Command: "./notify.sh", Type: "command"},
				},
				"PreCompact": []wire.Hook{
					{Command: "./compact.sh"},
				},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ := os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})

	if _, ok := hooks["Notification"]; !ok {
		t.Error("expected Notification hook from passthrough")
	}
	if _, ok := hooks["PreCompact"]; !ok {
		t.Error("expected PreCompact hook from passthrough")
	}
}
func TestClaudeCodeHookWriter_MCPServerInjection(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Empty config should still add MCP server
	cfg := &wire.HooksConfig{}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// MCP servers are now written to .mcp.json (not settings.json)
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("failed to read .mcp.json: %v", err)
	}

	var mcpConfig map[string]interface{}
	if err := json.Unmarshal(data, &mcpConfig); err != nil {
		t.Fatalf("failed to parse .mcp.json: %v", err)
	}

	mcpServers, ok := mcpConfig["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("expected mcpServers in .mcp.json")
	}

	ctxloomServer, ok := mcpServers["ctxloom"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'ctxloom' MCP server")
	}

	if _, ok := ctxloomServer["_ctxloom"]; !ok {
		t.Error("ctxloom MCP server should have _ctxloom marker")
	}

	if ctxloomServer["command"] == "" {
		t.Error("ctxloom MCP server should have command")
	}

	// Verify settings.json does NOT contain mcpServers
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		_ = json.Unmarshal(data, &settings)
		if _, ok := settings["mcpServers"]; ok {
			t.Error("settings.json should NOT contain mcpServers (they belong in .mcp.json)")
		}
	}
}
func TestClaudeCodeHookWriter_PreservesUserMCPServers(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing .mcp.json with user-defined MCP servers
	existingMCP := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"my-custom-server": map[string]interface{}{
				"command": "/usr/local/bin/my-mcp-server",
				"args":    []string{"--port", "3000"},
				// No _ctxloom field - user-defined
			},
			"another-server": map[string]interface{}{
				"command": "python",
				"args":    []string{"-m", "mcp_server"},
			},
		},
	}

	data, _ := json.MarshalIndent(existingMCP, "", "  ")
	_ = os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644)

	// Write hooks with ctxloom config
	cfg := &wire.HooksConfig{}
	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read updated .mcp.json
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	data, _ = os.ReadFile(mcpPath)

	var mcpConfig map[string]interface{}
	_ = json.Unmarshal(data, &mcpConfig)

	mcpServers := mcpConfig["mcpServers"].(map[string]interface{})

	// User servers should be preserved
	if _, ok := mcpServers["my-custom-server"]; !ok {
		t.Error("user-defined 'my-custom-server' should be preserved")
	}
	if _, ok := mcpServers["another-server"]; !ok {
		t.Error("user-defined 'another-server' should be preserved")
	}

	// ctxloom server should be added
	if _, ok := mcpServers["ctxloom"]; !ok {
		t.Error("ctxloom MCP server should be added")
	}

	// Verify total count
	if len(mcpServers) != 3 {
		t.Errorf("expected 3 MCP servers (2 user + 1 ctxloom), got %d", len(mcpServers))
	}
}
func TestClaudeCodeHookWriter_UpdatesSCMMCPServer(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing .mcp.json with old ctxloom MCP server
	existingMCP := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"ctxloom": map[string]interface{}{
				"command":  "/old/path/to/ctxloom mcp",
				"_ctxloom": "old-marker",
			},
			"user-server": map[string]interface{}{
				"command": "/usr/bin/user-mcp",
			},
		},
	}

	data, _ := json.MarshalIndent(existingMCP, "", "  ")
	_ = os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644)

	// Write hooks - should update ctxloom server
	cfg := &wire.HooksConfig{}
	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read updated .mcp.json
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	data, _ = os.ReadFile(mcpPath)

	var mcpConfig map[string]interface{}
	_ = json.Unmarshal(data, &mcpConfig)

	mcpServers := mcpConfig["mcpServers"].(map[string]interface{})

	// User server should be preserved
	if _, ok := mcpServers["user-server"]; !ok {
		t.Error("user-defined server should be preserved")
	}

	// ctxloom server should be updated (not duplicate)
	ctxloomServer := mcpServers["ctxloom"].(map[string]interface{})
	if ctxloomServer["command"] == "/old/path/to/ctxloom mcp" {
		t.Error("ctxloom server command should be updated")
	}
	if ctxloomServer["_ctxloom"] == "old-marker" {
		t.Error("ctxloom server marker should be updated")
	}

	// Should still have exactly 2 servers
	if len(mcpServers) != 2 {
		t.Errorf("expected 2 MCP servers, got %d", len(mcpServers))
	}
}
func TestClaudeCodeHookWriter_ResilienceToMalformedJSON(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	// Create malformed settings.json
	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte("{ invalid json }"), 0644))

	// WriteHooks should NOT fail - it should warn and continue
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "./test.sh"}},
		},
	}
	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err, "should not fail on malformed existing settings.json")

	// Verify hooks were still written
	data, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)

	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.Contains(t, settings, "hooks", "should have hooks after writing")
}
func TestClaudeCodeHookWriter_CreatesBackupBeforeModifying(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	// Create existing valid settings.json
	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	originalContent := `{"existingKey": "originalValue"}`
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(originalContent), 0644))

	// Write hooks
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "./test.sh"}},
		},
	}
	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err)

	// Verify backup was created
	backupPath := settingsPath + ".ctxloom.bak"
	exists, err := afero.Exists(fs, backupPath)
	require.NoError(t, err)
	assert.True(t, exists, "backup file should be created")

	// Verify backup contains original content
	backupData, err := afero.ReadFile(fs, backupPath)
	require.NoError(t, err)
	assert.Equal(t, originalContent, string(backupData), "backup should contain original content")
}
func TestClaudeCodeHookWriter_MCPConfigResilience(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	// Create malformed .mcp.json
	mcpPath := "/project/.mcp.json"
	require.NoError(t, afero.WriteFile(fs, mcpPath, []byte("not valid json"), 0644))

	// WriteHooks should NOT fail - it should warn and continue
	cfg := &wire.HooksConfig{}
	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err, "should not fail on malformed .mcp.json")

	// Verify MCP config was still written
	data, err := afero.ReadFile(fs, mcpPath)
	require.NoError(t, err)

	var mcpConfig map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &mcpConfig))
	assert.Contains(t, mcpConfig, "mcpServers", "should have mcpServers after writing")
}
