// Capability tests for the Claude Code launch backend (lifecycle, MCP, skills,
// context, history) plus the shared hook-assembly contract it drives.
package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaudeLifecycle_New verifies proper initialization
func TestClaudeLifecycle_New(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	lifecycle := NewClaudeLifecycle(backend)

	assert.NotNil(t, lifecycle)
	assert.Equal(t, backend, lifecycle.backend)
	assert.NotNil(t, lifecycle.BaseLifecycle)
}

// TestClaudeLifecycle_OnSessionStart verifies session start handler registration
func TestClaudeLifecycle_OnSessionStart(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	lifecycle := NewClaudeLifecycle(backend)

	handler := EventHandler{
		Command: "echo test",
		Timeout: 30,
	}

	err := lifecycle.OnSessionStart("/tmp", handler)
	require.NoError(t, err)

	// Verify hook was added via GetHooks()
	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionStart, 1)
}

// TestClaudeLifecycle_OnSessionEnd verifies session end handler registration
func TestClaudeLifecycle_OnSessionEnd(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	lifecycle := NewClaudeLifecycle(backend)

	handler := EventHandler{
		Command: "echo cleanup",
		Timeout: 30,
	}

	err := lifecycle.OnSessionEnd("/tmp", handler)
	require.NoError(t, err)

	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
	assert.Len(t, hooks.Unified.SessionEnd, 1)
}

// TestClaudeLifecycle_OnToolUse verifies tool use handler registration
func TestClaudeLifecycle_OnToolUse(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	lifecycle := NewClaudeLifecycle(backend)

	handler := EventHandler{
		Command: "echo tool",
		Timeout: 30,
	}

	t.Run("before tool use", func(t *testing.T) {
		err := lifecycle.OnToolUse("/tmp", BeforeToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle.GetHooks()
		assert.Len(t, hooks.Unified.PreTool, 1)
	})

	t.Run("after tool use", func(t *testing.T) {
		// Create fresh lifecycle for independent test
		lifecycle2 := NewClaudeLifecycle(backend)
		err := lifecycle2.OnToolUse("/tmp", AfterToolUse, handler)
		require.NoError(t, err)
		hooks := lifecycle2.GetHooks()
		assert.Len(t, hooks.Unified.PostTool, 1)
	})
}

// TestClaudeLifecycle_Clear verifies handlers can be cleared
func TestClaudeLifecycle_Clear(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	lifecycle := NewClaudeLifecycle(backend)

	// Add some hooks first
	_ = lifecycle.OnSessionStart("/tmp", EventHandler{Command: "echo test"})

	// Note: Clear will try to write to settings, which may fail in test
	// We're just verifying it resets internal state
	_ = lifecycle.Clear("/tmp")
	hooks := lifecycle.GetHooks()
	assert.NotNil(t, hooks)
}

// TestClaudeLifecycle_Flush verifies hooks and MCP are flushed
func TestClaudeLifecycle_Flush(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	lifecycle := NewClaudeLifecycle(backend)

	// Add some hooks
	_ = lifecycle.OnSessionStart("/tmp", EventHandler{Command: "echo test"})

	// Flush will attempt file I/O; we're verifying it doesn't panic
	_ = lifecycle.Flush("/tmp")
}

func TestClaudeMCPManager_RegisterServer(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	manager := NewClaudeMCPManager(backend)

	server := MCPServer{
		Name:    "test-server",
		Command: "test-cmd",
		Args:    []string{"arg1"},
	}

	err := manager.RegisterServer("/tmp", server)
	require.NoError(t, err)

	servers, _ := manager.ListServers("/tmp")
	assert.Len(t, servers, 1)
	assert.Contains(t, servers, "test-server")
}

func TestClaudeMCPManager_UnregisterServer(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	manager := NewClaudeMCPManager(backend)

	// Register first
	server := MCPServer{
		Name:    "test-server",
		Command: "test-cmd",
	}
	_ = manager.RegisterServer("/tmp", server)

	err := manager.UnregisterServer("/tmp", "test-server")
	require.NoError(t, err)

	servers, _ := manager.ListServers("/tmp")
	assert.Len(t, servers, 0)
}

func TestClaudeMCPManager_ListServers(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	manager := NewClaudeMCPManager(backend)

	_ = manager.RegisterServer("/tmp", MCPServer{Name: "server1"})
	_ = manager.RegisterServer("/tmp", MCPServer{Name: "server2"})

	names, err := manager.ListServers("/tmp")
	require.NoError(t, err)
	assert.Len(t, names, 2)
	assert.Contains(t, names, "server1")
	assert.Contains(t, names, "server2")
}

func TestClaudeMCPManager_GetServer(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	manager := NewClaudeMCPManager(backend)

	server := MCPServer{
		Name:    "test-server",
		Command: "test-cmd",
		Args:    []string{"arg1"},
	}
	_ = manager.RegisterServer("/tmp", server)

	result, err := manager.GetServer("/tmp", "test-server")
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, server.Name, result.Name)
	assert.Equal(t, server.Command, result.Command)
}

func TestClaudeMCPManager_GetServer_NotFound(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	manager := NewClaudeMCPManager(backend)

	result, err := manager.GetServer("/tmp", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestClaudeMCPManager_Clear(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	manager := NewClaudeMCPManager(backend)

	_ = manager.RegisterServer("/tmp", MCPServer{Name: "server1"})

	// Clear will attempt file I/O; we're verifying it clears internal state
	_ = manager.Clear("/tmp")
	servers, _ := manager.ListServers("/tmp")
	assert.Len(t, servers, 0)
}

func TestClaudeSkills_Register(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	skills := &ClaudeSkills{
		backend: backend,
	}

	skill := Skill{
		Name:        "test-skill",
		Description: "Test skill",
		Content:     "# Test Skill\n\nTest content",
	}

	// Register will attempt file I/O
	err := skills.Register("/tmp", skill)
	// We expect this to succeed or fail due to file I/O, not panic
	_ = err
}

func TestClaudeSkills_RegisterAll(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	skills := &ClaudeSkills{
		backend: backend,
	}

	skillList := []Skill{
		{
			Name:        "skill1",
			Description: "Skill 1",
			Content:     "# Skill 1",
		},
		{
			Name:        "skill2",
			Description: "Skill 2",
			Content:     "# Skill 2",
		},
	}

	// RegisterAll will attempt file I/O
	err := skills.RegisterAll("/tmp", skillList)
	// We expect this to succeed or fail due to file I/O, not panic
	_ = err
}

// TestClaudeSkills_Register_WritesToWorkdir exercises the on-disk
// effect of Register against a tempdir, then asserts the slash-
// command file and manifest entry land where we expect. Tightens the
// existing "doesn't panic" smoke test.
func TestClaudeSkills_Register_WritesToWorkdir(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode(writeClaudeSettings)}
	require.NoError(t, skills.Register(workDir, Skill{
		Name:        "test-skill",
		Description: "Test skill",
		Content:     "# Test Skill\n\nTest content",
	}))

	// Slash command file must exist somewhere under .claude/commands/.
	cmds, err := os.ReadDir(filepath.Join(workDir, ".claude", "commands"))
	require.NoError(t, err)
	var found bool
	for _, e := range cmds {
		if strings.Contains(e.Name(), "test-skill") {
			found = true
		}
	}
	assert.True(t, found, "test-skill command file must land under .claude/commands/")

	// Manifest must track it for later Clear/List.
	manifest, err := os.ReadFile(filepath.Join(workDir, ".claude", "commands", ".ctxloom-manifest"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "test-skill")
}

// TestClaudeSkills_RegisterFromContent covers the host-resolved export path: the
// host maps bundle content to agent.CommandExport (enablement + metadata already
// resolved) and the skill writer just emits the command files.
func TestClaudeSkills_RegisterFromContent(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode(writeClaudeSettings)}

	require.NoError(t, skills.RegisterFromContent(workDir, []agent.CommandExport{
		{Name: "from-bundle", Content: "body", Enabled: true, Description: "From a bundle"},
	}))
	// Check the manifest tracks it.
	manifest, err := os.ReadFile(filepath.Join(workDir, ".claude", "commands", ".ctxloom-manifest"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "from-bundle")
}

// TestClaudeSkills_List_NoManifest returns empty + nil when nothing
// has been registered yet.
func TestClaudeSkills_List_NoManifest(t *testing.T) {
	skills := &ClaudeSkills{backend: NewClaudeCode(writeClaudeSettings)}
	names, err := skills.List(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, names)
}

// TestClaudeSkills_List_StripsExtension reads back what Register wrote
// and confirms the .md suffix is gone from the returned names.
func TestClaudeSkills_List_StripsExtension(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode(writeClaudeSettings)}
	require.NoError(t, skills.RegisterAll(workDir, []Skill{
		{Name: "a", Content: "x"},
		{Name: "b", Content: "y"},
	}))

	names, err := skills.List(workDir)
	require.NoError(t, err)
	for _, n := range names {
		assert.NotContains(t, n, ".md", "List must strip the .md suffix")
	}
	// Both registered skills should appear.
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	// names from WriteCommandFiles include subdir prefixes; check for
	// any name containing "a" or "b".
	var hasA, hasB bool
	for n := range got {
		if strings.HasSuffix(n, "a") {
			hasA = true
		}
		if strings.HasSuffix(n, "b") {
			hasB = true
		}
	}
	assert.True(t, hasA && hasB, "both registered skills must surface in List: %v", names)
}

// TestClaudeSkills_Clear removes registered skills + manifest. After
// Clear, List returns empty again.
func TestClaudeSkills_Clear(t *testing.T) {
	workDir := t.TempDir()
	skills := &ClaudeSkills{backend: NewClaudeCode(writeClaudeSettings)}
	require.NoError(t, skills.Register(workDir, Skill{
		Name: "doomed", Content: "x", Description: "to be cleared",
	}))

	// Sanity check
	before, _ := skills.List(workDir)
	require.NotEmpty(t, before, "fixture should have produced an entry")

	require.NoError(t, skills.Clear(workDir))

	after, err := skills.List(workDir)
	require.NoError(t, err)
	assert.Empty(t, after, "Clear must remove all tracked skills")

	// Manifest itself is gone.
	_, err = os.Stat(filepath.Join(workDir, ".claude", "commands", ".ctxloom-manifest"))
	assert.True(t, os.IsNotExist(err), "manifest file must be removed")
}

// TestClaudeSkills_Clear_NoManifest is a no-op when no manifest exists
// (fresh workdir).
func TestClaudeSkills_Clear_NoManifest(t *testing.T) {
	skills := &ClaudeSkills{backend: NewClaudeCode(writeClaudeSettings)}
	require.NoError(t, skills.Clear(t.TempDir()), "Clear without manifest must succeed")
}

func TestClaudeContext_GetContextHash(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	context := NewClaudeContext(backend)

	// Write context to set hash
	fragments := []*Fragment{{Content: "test content"}}
	_ = context.Provide("/tmp", fragments)

	hash := context.GetContextHash()
	assert.NotEmpty(t, hash)
}

func TestClaudeContext_GetContextHash_Empty(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	context := NewClaudeContext(backend)

	hash := context.GetContextHash()
	assert.Equal(t, "", hash)
}

func TestClaudeContext_GetContextFilePath_Empty(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	context := NewClaudeContext(backend)

	path := context.GetContextFilePath()
	assert.Equal(t, "", path)
}

func TestClaudeContext_GetContextFilePath_WithHash(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	context := NewClaudeContext(backend)

	// Provide context to generate a hash
	tmpDir := t.TempDir()
	_ = context.Provide(tmpDir, []*Fragment{{Content: "test content"}})

	path := context.GetContextFilePath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, SCMContextSubdir)
	assert.Contains(t, path, ".md")
}

func TestClaudeContext_Clear(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	context := NewClaudeContext(backend)

	// Provide some context first
	_ = context.Provide("/tmp", []*Fragment{{Content: "test"}})

	err := context.Clear("/tmp")
	require.NoError(t, err)
	assert.Equal(t, "", context.GetContextHash())
}

// The agent-side seam: MergeManaged folds the host-assembled (wire-typed)
// ManagedConfig into the lifecycle and appends the agent's own context-injection
// hook. The config/profile/bundle resolution that produces ManagedConfig is
// covered host-side in internal/lm/backends (managed_test.go).

func TestClaudeLifecycle_MergeManaged_AppendsContextInjection(t *testing.T) {
	lifecycle := NewClaudeLifecycle(NewClaudeCode(writeClaudeSettings))

	lifecycle.MergeManaged(&agent.ManagedConfig{
		Hooks: &wire.HooksConfig{Plugins: map[string]wire.BackendHooks{}},
	}, "/tmp", "abc123hash")

	hooks := lifecycle.GetHooks()
	var hasInject bool
	for _, h := range hooks.Unified.SessionStart {
		if strings.Contains(h.Command, "inject-context") {
			hasInject = true
		}
	}
	assert.True(t, hasInject, "context-injection hook must be appended when contextHash is set")
}

func TestClaudeLifecycle_MergeManaged_NoContextHash(t *testing.T) {
	lifecycle := NewClaudeLifecycle(NewClaudeCode(writeClaudeSettings))

	// Host-assembled SessionStart hooks (e.g. bundle `hook session-bind`) ride in
	// via ManagedConfig.Hooks; the agent appends only the context-injection hook,
	// and only when a hash is present.
	lifecycle.MergeManaged(&agent.ManagedConfig{
		Hooks: &wire.HooksConfig{
			Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind"}},
			},
			Plugins: map[string]wire.BackendHooks{},
		},
	}, "/tmp", "")

	hooks := lifecycle.GetHooks()
	for _, h := range hooks.Unified.SessionStart {
		assert.NotContains(t, h.Command, "inject-context",
			"context-injection hook must not be added when contextHash is empty")
	}
	var hasBind bool
	for _, h := range hooks.Unified.SessionStart {
		if strings.Contains(h.Command, "session-bind") {
			hasBind = true
		}
	}
	assert.True(t, hasBind, "host-assembled SessionStart hooks must be preserved")
}

func TestClaudeLifecycle_MergeManaged_MergesHooksAndMCP(t *testing.T) {
	lifecycle := NewClaudeLifecycle(NewClaudeCode(writeClaudeSettings))

	lifecycle.MergeManaged(&agent.ManagedConfig{
		Hooks: &wire.HooksConfig{
			Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "profile-hook"}}},
			Plugins: map[string]wire.BackendHooks{},
		},
		MCP: &wire.MCPConfig{
			Servers: map[string]wire.MCPServer{"profile-mcp": {Command: "profile-mcp-cmd"}},
		},
	}, "/tmp", "hash123")

	hooks := lifecycle.GetHooks()
	assert.Len(t, hooks.Unified.PreTool, 1)
	assert.Equal(t, "profile-hook", hooks.Unified.PreTool[0].Command)

	mcp := lifecycle.GetMCP()
	assert.Contains(t, mcp.Servers, "profile-mcp")
}

// TestClaudeLifecycle_MergeManaged_Statusline verifies the manage_statusline
// payload bit drives the writer end-to-end through the real settings.json write:
// ManageStatusline=true installs the ctxloom statusline, false omits it. This is
// the statusline's full path now that the host (not the plugin reading config)
// decides whether to manage it.
func TestClaudeLifecycle_MergeManaged_Statusline(t *testing.T) {
	t.Run("managed installs statusline", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		lifecycle := NewClaudeLifecycle(NewClaudeCode(memWriteClaudeSettings(fs)))
		lifecycle.MergeManaged(&agent.ManagedConfig{ManageStatusline: true}, "/proj", "")
		require.NoError(t, lifecycle.Flush("/proj"))

		data, err := afero.ReadFile(fs, filepath.Join("/proj", ".claude", "settings.json"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "statusLine", "ManageStatusline=true must write a ctxloom statusline")
	})

	t.Run("opt-out omits statusline", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		lifecycle := NewClaudeLifecycle(NewClaudeCode(memWriteClaudeSettings(fs)))
		lifecycle.MergeManaged(&agent.ManagedConfig{ManageStatusline: false}, "/proj", "")
		require.NoError(t, lifecycle.Flush("/proj"))

		data, err := afero.ReadFile(fs, filepath.Join("/proj", ".claude", "settings.json"))
		require.NoError(t, err)
		assert.NotContains(t, string(data), "statusLine", "ManageStatusline=false must not write a statusline")
	})
}

func TestClaudeLifecycle_MergeManaged_NilIsNoOp(t *testing.T) {
	lifecycle := NewClaudeLifecycle(NewClaudeCode(writeClaudeSettings))
	lifecycle.MergeManaged(nil, "/tmp", "hash123") // must not panic
	assert.Nil(t, lifecycle.GetHooks(), "nil managed config must not initialize hook state")
}

func TestClaudeLifecycle_GetMCP(t *testing.T) {
	lifecycle := NewClaudeLifecycle(NewClaudeCode(writeClaudeSettings))

	// Initially nil
	assert.Nil(t, lifecycle.GetMCP())

	// After merging a managed config carrying MCP servers.
	lifecycle.MergeManaged(&agent.ManagedConfig{
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{"test-server": {Command: "test"}}},
	}, "/tmp", "")

	assert.NotNil(t, lifecycle.GetMCP())
}

func TestClaudeCode_History(t *testing.T) {
	backend := NewClaudeCode(writeClaudeSettings)
	history := backend.History()
	assert.NotNil(t, history)
}
