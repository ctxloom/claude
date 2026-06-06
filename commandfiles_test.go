package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/shared/agent"
)

func TestTransformMustacheToPositional(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"single variable", "Review {{file}}", "Review $1"},
		{"two variables", "Review {{file}} focusing on {{focus}}", "Review $1 focusing on $2"},
		{"repeated variable", "Check {{file}}, then recheck {{file}}", "Check $1, then recheck $1"},
		{"mixed order", "First {{a}}, then {{b}}, back to {{a}}", "First $1, then $2, back to $1"},
		{"no variables", "Just plain text", "Just plain text"},
		{"multiline", "Review {{file}}\n\nFocus: {{focus}}\n\nFile: {{file}}", "Review $1\n\nFocus: $2\n\nFile: $1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := transformMustacheToPositional(tt.input)
			if result != tt.expected {
				t.Errorf("transformMustacheToPositional(%q)\ngot:  %q\nwant: %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTransformToClaudeCommand(t *testing.T) {
	tests := []struct {
		name     string
		cmd      agent.CommandExport
		contains []string
		excludes []string
	}{
		{
			name: "full frontmatter",
			cmd: agent.CommandExport{
				Name:         "review",
				Content:      "Review {{file}} for {{focus}}",
				Description:  "Code review",
				ArgumentHint: "[file] [focus]",
				AllowedTools: []string{"Read", "Grep"},
				Model:        "claude-sonnet-4-20250514",
			},
			contains: []string{
				"---",
				"description: Code review",
				"argument-hint: [file] [focus]",
				"allowed-tools: Read, Grep",
				"model: claude-sonnet-4-20250514",
				"Review $1 for $2",
			},
		},
		{
			name:     "no frontmatter",
			cmd:      agent.CommandExport{Name: "simple", Content: "Just do the thing"},
			excludes: []string{"---"},
			contains: []string{"Just do the thing"},
		},
		{
			name: "partial frontmatter",
			cmd: agent.CommandExport{
				Name:        "partial",
				Content:     "Review the code",
				Description: "Quick review",
			},
			contains: []string{"---", "description: Quick review", "Review the code"},
			excludes: []string{"argument-hint:", "allowed-tools:", "model:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TransformToClaudeCommand(tt.cmd)
			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("expected result to contain %q\nresult: %s", s, result)
				}
			}
			for _, s := range tt.excludes {
				if strings.Contains(result, s) {
					t.Errorf("expected result to NOT contain %q\nresult: %s", s, result)
				}
			}
		})
	}
}

func TestWriteCommandFiles(t *testing.T) {
	tmpDir := t.TempDir()

	cmds := []agent.CommandExport{
		{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		{Name: "disabled", Content: "This should not be exported", Enabled: false},
		{Name: "simple", Content: "Simple command", Enabled: true},
	}

	if err := WriteCommandFiles(tmpDir, cmds); err != nil {
		t.Fatalf("WriteCommandFiles failed: %v", err)
	}

	reviewPath := filepath.Join(tmpDir, ".claude", "commands", "review.md")
	if _, err := os.Stat(reviewPath); os.IsNotExist(err) {
		t.Error("expected review.md to be created")
	}
	simplePath := filepath.Join(tmpDir, ".claude", "commands", "simple.md")
	if _, err := os.Stat(simplePath); os.IsNotExist(err) {
		t.Error("expected simple.md to be created")
	}
	disabledPath := filepath.Join(tmpDir, ".claude", "commands", "disabled.md")
	if _, err := os.Stat(disabledPath); !os.IsNotExist(err) {
		t.Error("expected disabled.md to NOT be created")
	}

	content, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatalf("failed to read review.md: %v", err)
	}
	if !strings.Contains(string(content), "description: Code review") {
		t.Error("review.md should contain description")
	}
	if !strings.Contains(string(content), "Review $1") {
		t.Error("review.md should have {{file}} transformed to $1")
	}
}

func TestWriteCommandFilesCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	commandsDir := filepath.Join(tmpDir, ".claude", "commands")
	manifestPath := filepath.Join(commandsDir, ".ctxloom-manifest")

	_ = os.MkdirAll(commandsDir, 0755)
	stalePath := filepath.Join(commandsDir, "stale.md")
	_ = os.WriteFile(stalePath, []byte("stale content"), 0644)
	_ = os.WriteFile(manifestPath, []byte("stale.md"), 0644)

	if err := WriteCommandFiles(tmpDir, []agent.CommandExport{{Name: "new", Content: "New content", Enabled: true}}); err != nil {
		t.Fatalf("WriteCommandFiles failed: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("expected stale.md to be removed")
	}
	newPath := filepath.Join(commandsDir, "new.md")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Error("expected new.md to be created")
	}
}

func TestWriteCommandFilesEmptyPrompts(t *testing.T) {
	tmpDir := t.TempDir()
	commandsDir := filepath.Join(tmpDir, ".claude", "commands")
	manifestPath := filepath.Join(commandsDir, ".ctxloom-manifest")

	_ = os.MkdirAll(commandsDir, 0755)
	stalePath := filepath.Join(commandsDir, "stale.md")
	_ = os.WriteFile(stalePath, []byte("stale content"), 0644)
	_ = os.WriteFile(manifestPath, []byte("stale.md"), 0644)

	if err := WriteCommandFiles(tmpDir, nil); err != nil {
		t.Fatalf("WriteCommandFiles failed: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("expected stale.md to be removed")
	}
}

func TestEscapeYAMLString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with spaces", "with spaces"},
		{"with: colon", `"with: colon"`},
		{"with #hash", `"with #hash"`},
		{" leading space", `" leading space"`},
		{"trailing space ", `"trailing space "`},
		{`has "quotes"`, `"has \"quotes\""`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeYAMLString(tt.input)
			if result != tt.expected {
				t.Errorf("escapeYAMLString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
