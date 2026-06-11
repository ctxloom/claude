package claude

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/ctxloom/shared/agent"
)

// WriteCommandFiles generates Claude Code slash command files from exported
// prompts. Files are written directly to .claude/commands/ (e.g., save.md ->
// /save). ctxloom tracks which files it manages via a manifest to clean up
// stale commands. Only exports with Enabled == true are written. The
// .claude/commands/ directory is shared with the user's own commands, so
// cleanup is manifest-scoped rather than a wipe (see
// agent.WriteManagedCommandFiles for the shared mechanics).
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	commandsDir := filepath.Join(workDir, ".claude", "commands")

	// Migration: remove the old ctxloom/ subdirectory from earlier versions
	// that wrote commands there instead of flat with a manifest.
	_ = fs.RemoveAll(filepath.Join(commandsDir, "ctxloom"))

	return agent.WriteManagedCommandFiles(fs, commandsDir, ".ctxloom-manifest", cmds,
		func(c agent.CommandExport) (string, []byte, error) {
			// Replace path separators with dashes for nested names.
			filename := strings.ReplaceAll(c.Name, "/", "-") + ".md"
			return filename, []byte(TransformToClaudeCommand(c)), nil
		})
}

// TransformToClaudeCommand converts a command export to Claude Code command
// format: a markdown file with YAML frontmatter and {{var}} transformed to $N.
func TransformToClaudeCommand(c agent.CommandExport) string {
	var buf bytes.Buffer

	hasFrontmatter := c.Description != "" ||
		c.ArgumentHint != "" ||
		len(c.AllowedTools) > 0 ||
		c.Model != ""

	if hasFrontmatter {
		buf.WriteString("---\n")

		if c.Description != "" {
			buf.WriteString("description: ")
			buf.WriteString(agent.EscapeYAMLString(c.Description))
			buf.WriteString("\n")
		}

		if c.ArgumentHint != "" {
			buf.WriteString("argument-hint: ")
			buf.WriteString(c.ArgumentHint)
			buf.WriteString("\n")
		}

		if len(c.AllowedTools) > 0 {
			buf.WriteString("allowed-tools: ")
			buf.WriteString(strings.Join(c.AllowedTools, ", "))
			buf.WriteString("\n")
		}

		if c.Model != "" {
			buf.WriteString("model: ")
			buf.WriteString(c.Model)
			buf.WriteString("\n")
		}

		buf.WriteString("---\n\n")
	}

	buf.WriteString(agent.TransformMustacheToPositional(c.Content))
	return buf.String()
}
