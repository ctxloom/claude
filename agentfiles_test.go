package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeAgentName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"reviewer", "reviewer"},
		{"research/Deep Reviewer", "deep-reviewer"},
		{"Test_Writer", "test-writer"},
		{"bundle/Code Review!!", "code-review"},
		{"--Weird--", "weird"},
		{"/abs", "abs"}, // leading slash: leaf stripped, then hyphen-trimmed
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, claudeAgentName(tt.in))
		})
	}
}

func TestTransformToClaudeAgent(t *testing.T) {
	tests := []struct {
		name     string
		export   AgentExport
		contains []string
		excludes []string
	}{
		{
			name: "full frontmatter",
			export: AgentExport{
				Name:         "research/reviewer",
				Profile:      "deep-review",
				Description:  "Use for thorough code review",
				SystemPrompt: "You are a meticulous reviewer.",
				Tools:        []string{"Read", "Grep", "Glob"},
				Model:        "opus",
			},
			contains: []string{
				"---",
				"name: reviewer",
				"description: Use for thorough code review",
				"tools: Read, Grep, Glob",
				"model: opus",
				"You are a meticulous reviewer.",
			},
		},
		{
			name: "tools omitted means inherit all",
			export: AgentExport{
				Name:         "helper",
				Description:  "general helper",
				SystemPrompt: "Help out.",
			},
			contains: []string{"name: helper", "description: general helper", "Help out."},
			excludes: []string{"tools:", "model:"},
		},
		{
			name: "system prompt gets trailing newline",
			export: AgentExport{
				Name:         "x",
				SystemPrompt: "no newline",
			},
			contains: []string{"no newline\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TransformToClaudeAgent(tt.export)
			for _, s := range tt.contains {
				assert.Contains(t, result, s)
			}
			for _, s := range tt.excludes {
				assert.NotContains(t, result, s)
			}
		})
	}
}

func TestWriteAgentFiles(t *testing.T) {
	tmpDir := t.TempDir()

	agents := []AgentExport{
		{Name: "reviewer", Profile: "review", Enabled: true, Description: "Reviews code", SystemPrompt: "Review.", Tools: []string{"Read", "Grep"}, Model: "opus"},
		{Name: "disabled", Enabled: false, SystemPrompt: "should not appear"},
		{Name: "research/deep", Enabled: true, SystemPrompt: "Research deeply."},
	}

	require.NoError(t, WriteAgentFiles(tmpDir, agents))

	agentsDir := filepath.Join(tmpDir, ".claude", "agents")

	reviewer, err := os.ReadFile(filepath.Join(agentsDir, "reviewer.md"))
	require.NoError(t, err)
	assert.Contains(t, string(reviewer), "name: reviewer")
	assert.Contains(t, string(reviewer), "tools: Read, Grep")
	assert.Contains(t, string(reviewer), "model: opus")
	assert.Contains(t, string(reviewer), "Review.")

	// The filename is the same slug used as the frontmatter name (the leaf of a
	// nested roster name, slugified), so file identity == sub-agent identity.
	deep, err := os.ReadFile(filepath.Join(agentsDir, "deep.md"))
	require.NoError(t, err)
	assert.Contains(t, string(deep), "name: deep")

	_, err = os.Stat(filepath.Join(agentsDir, "disabled.md"))
	assert.True(t, os.IsNotExist(err), "disabled agent must not be written")

	manifest, err := os.ReadFile(filepath.Join(agentsDir, ".ctxloom-agents-manifest"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "reviewer.md")
	assert.Contains(t, string(manifest), "deep.md")
}

// The on-disk filename must be the same slug used as the frontmatter `name`, so
// a roster name with uppercase/spaces/punctuation does not produce a file whose
// identity disagrees with the sub-agent it declares. claude-code-01-004.
func TestWriteAgentFiles_FilenameMatchesSlug(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, WriteAgentFiles(tmpDir, []AgentExport{
		{Name: "Team/Code Reviewer", Enabled: true, SystemPrompt: "x"},
	}))

	agentsDir := filepath.Join(tmpDir, ".claude", "agents")
	body, err := os.ReadFile(filepath.Join(agentsDir, "code-reviewer.md"))
	require.NoError(t, err, "file is named by the frontmatter slug, not the raw name")
	assert.Contains(t, string(body), "name: code-reviewer")
}

func TestWriteAgentFilesCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))

	stale := filepath.Join(agentsDir, "stale.md")
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, ".ctxloom-agents-manifest"), []byte("stale.md"), 0644))

	require.NoError(t, WriteAgentFiles(tmpDir, []AgentExport{{Name: "fresh", Enabled: true, SystemPrompt: "x"}}))

	_, err := os.Stat(stale)
	assert.True(t, os.IsNotExist(err), "stale agent file must be removed via manifest")
	_, err = os.Stat(filepath.Join(agentsDir, "fresh.md"))
	assert.NoError(t, err)
}

// User-authored sub-agents in .claude/agents/ (not in ctxloom's manifest) must
// survive a rewrite — the directory is shared territory.
func TestWriteAgentFiles_PreservesUserAuthored(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))

	userAgent := filepath.Join(agentsDir, "my-own.md")
	require.NoError(t, os.WriteFile(userAgent, []byte("mine"), 0644))

	require.NoError(t, WriteAgentFiles(tmpDir, []AgentExport{{Name: "managed", Enabled: true, SystemPrompt: "x"}}))

	_, err := os.Stat(userAgent)
	assert.NoError(t, err, "user-authored sub-agent must be preserved")
}

func TestWriteAgentFiles_SkipsTraversalNames(t *testing.T) {
	tmpDir := t.TempDir()
	agents := []AgentExport{
		{Name: "../escape", Enabled: true, SystemPrompt: "evil"},
		{Name: "/abs", Enabled: true, SystemPrompt: "evil"},
		{Name: "good", Enabled: true, SystemPrompt: "fine"},
	}
	require.NoError(t, WriteAgentFiles(tmpDir, agents))

	agentsDir := filepath.Join(tmpDir, ".claude", "agents")
	_, err := os.Stat(filepath.Join(agentsDir, "good.md"))
	assert.NoError(t, err)

	entries, err := os.ReadDir(agentsDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "escape")
		assert.NotContains(t, e.Name(), "abs")
	}
}

func TestWriteAgentFiles_EmptyRemovesManaged(t *testing.T) {
	tmpDir := t.TempDir()
	agentsDir := filepath.Join(tmpDir, ".claude", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "old.md"), []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, ".ctxloom-agents-manifest"), []byte("old.md"), 0644))

	require.NoError(t, WriteAgentFiles(tmpDir, nil))

	_, err := os.Stat(filepath.Join(agentsDir, "old.md"))
	assert.True(t, os.IsNotExist(err))
}

func TestAgentExport_MCPServersCarriedNotRendered(t *testing.T) {
	// MCP servers are agent-scoped in the managed-agents model, but Claude Code
	// CLI sub-agents can't scope MCP, so the field is carried for the abstraction
	// and NOT emitted into the .md.
	out := TransformToClaudeAgent(AgentExport{
		Name:         "r",
		SystemPrompt: "x",
		MCPServers:   []string{"github", "spotify"},
	})
	assert.NotContains(t, out, "github")
	assert.NotContains(t, out, "mcp")
	assert.False(t, strings.Contains(out, "spotify"))
}
