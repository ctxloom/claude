package claude

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
)

// WriteCommandFiles generates Claude Code slash command files from exported
// prompts. Files are written directly to .claude/commands/ (e.g., save.md ->
// /save). ctxloom tracks which files it manages via a manifest to clean up
// stale commands. Only exports with Enabled == true are written. The
// .claude/commands/ directory is shared with the user's own commands, so
// cleanup is manifest-scoped rather than a wipe.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)

	commandsDir := filepath.Join(workDir, ".claude", "commands")
	manifestPath := filepath.Join(commandsDir, ".ctxloom-manifest")

	cleanupTrackedCommands(fs, commandsDir, manifestPath)

	if !hasExportableCommands(cmds) {
		_ = fs.Remove(manifestPath)
		return nil
	}

	if err := fs.MkdirAll(commandsDir, 0755); err != nil {
		return fmt.Errorf("create commands dir: %w", err)
	}

	manifest, err := writeClaudeCommands(fs, commandsDir, cmds)
	if err != nil {
		return err
	}

	if err := afero.WriteFile(fs, manifestPath, []byte(strings.Join(manifest, "\n")), 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return nil
}

// cleanupTrackedCommands removes the old ctxloom/ subdirectory (migration) and
// every command file recorded in the previous run's manifest.
func cleanupTrackedCommands(fs afero.Fs, commandsDir, manifestPath string) {
	_ = fs.RemoveAll(filepath.Join(commandsDir, "ctxloom"))

	data, err := afero.ReadFile(fs, manifestPath)
	if err != nil {
		return
	}
	for _, name := range strings.Split(string(data), "\n") {
		if name = strings.TrimSpace(name); name != "" {
			_ = fs.Remove(filepath.Join(commandsDir, name))
		}
	}
}

// hasExportableCommands reports whether any export is enabled.
func hasExportableCommands(cmds []agent.CommandExport) bool {
	for _, c := range cmds {
		if c.Enabled {
			return true
		}
	}
	return false
}

// writeClaudeCommands writes each enabled export as a Claude command file,
// returning the manifest of written filenames.
func writeClaudeCommands(fs afero.Fs, commandsDir string, cmds []agent.CommandExport) ([]string, error) {
	var manifest []string
	for _, c := range cmds {
		if !c.Enabled {
			continue
		}

		md := TransformToClaudeCommand(c)
		// Replace path separators with dashes for nested names.
		filename := strings.ReplaceAll(c.Name, "/", "-") + ".md"
		if err := afero.WriteFile(fs, filepath.Join(commandsDir, filename), []byte(md), 0644); err != nil {
			return nil, fmt.Errorf("write command %s: %w", c.Name, err)
		}
		manifest = append(manifest, filename)
	}
	return manifest, nil
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
			buf.WriteString(escapeYAMLString(c.Description))
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

	buf.WriteString(transformMustacheToPositional(c.Content))
	return buf.String()
}

// transformMustacheToPositional replaces {{variable}} patterns with $1, $2, etc.
// Variables are assigned positions based on their first occurrence order.
func transformMustacheToPositional(content string) string {
	varNum := 1
	seen := make(map[string]int)
	re := regexp.MustCompile(`\{\{(\w+)\}\}`)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		varName := re.FindStringSubmatch(match)[1]
		if num, exists := seen[varName]; exists {
			return fmt.Sprintf("$%d", num)
		}
		seen[varName] = varNum
		num := varNum
		varNum++
		return fmt.Sprintf("$%d", num)
	})
}

// escapeYAMLString quotes a string for safe inclusion in YAML when it contains
// special characters.
func escapeYAMLString(s string) string {
	needsQuotes := strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`") ||
		strings.HasPrefix(s, " ") ||
		strings.HasSuffix(s, " ") ||
		strings.Contains(s, "\n")

	if needsQuotes {
		escaped := strings.ReplaceAll(s, `"`, `\"`)
		return `"` + escaped + `"`
	}
	return s
}
