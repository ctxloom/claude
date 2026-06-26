package claude

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ctxloom/shared/agent"
)

// This file abstracts a multi-agent roster (the model behind the Managed Agents
// "coordinator + agents" API, https://platform.claude.com/docs/en/managed-agents/multi-agent)
// into a ctxloom bundle component, materialized for the Claude Code CLI as
// `.claude/agents/<name>.md` sub-agent definition files.
//
// The managed-agents model says each agent carries "its own configuration
// (model, system prompt, tools, MCP servers, and skills)". ctxloom already has a
// noun for exactly that bundle of config: a profile. So the abstraction is —
// a roster lists agents, and EACH AGENT TAKES A PROFILE that supplies its
// model/tools/MCP/system-prompt. The host resolves the profile to concrete
// fields (the same way it resolves a CommandExport's Enabled/Model) and hands
// the backend a resolved AgentExport; this file only renders + writes it.
//
// It deliberately mirrors commandfiles.go: same manifest-scoped, traversal-safe
// write machinery (agent.WriteManagedCommandFiles), so `.claude/agents/` — which
// is shared with the user's own sub-agents — is never wiped wholesale.

// ClaudeAgents registers a multi-agent roster for Claude Code, symmetric to
// ClaudeSkills (which registers slash commands). The host maps a bundle's roster
// component — each entry binding an agent to a ctxloom profile — to resolved
// AgentExports for claude-code, so this stays config/bundle/profile-free.
//
// Wiring note: ctxloom calls RegisterRoster from Setup once the host-assembled
// ManagedConfig carries a roster (the agent-agnostic AgentExport list, the
// analog of ManagedConfig.Prompts for commands). That field lives in
// ctxloom/shared; until it lands, RegisterRoster is callable directly.
type ClaudeAgents struct{}

// RegisterRoster writes Claude Code sub-agent definition files from
// host-resolved roster entries.
func (a *ClaudeAgents) RegisterRoster(workDir string, roster []AgentExport) error {
	return WriteAgentFiles(workDir, roster)
}

// AgentExport is the agent-agnostic spec for one roster member. It is the
// abstraction the per-backend agent-definition writers consume, so they never
// import ctxloom's bundle/profile types: ctxloom maps each bundle roster entry
// to an AgentExport for the target backend (resolving the bound profile to
// model/tools/MCP + enablement) at the wiring boundary. A backend that cannot
// honor a given field simply ignores it.
type AgentExport struct {
	Name         string   // Roster name (bundle/item); path separators allowed
	Profile      string   // ctxloom profile bound to this agent (provenance; resolution is host-side)
	Enabled      bool     // Resolved enablement for the target backend
	Description  string   // When the coordinator should delegate here (drives Claude Code auto-routing)
	SystemPrompt string   // The agent's system prompt (the file body)
	Tools        []string // Tool allowlist; empty means "inherit all" in Claude Code
	Model        string   // Model alias/id for this agent (e.g. "sonnet", "opus", "haiku")
	// MCPServers is the agent-scoped server set from the managed-agents model
	// ("MCP servers are agent-scoped"). Claude Code's CLI sub-agents cannot scope
	// MCP per sub-agent (MCP is session-global there), so this is carried for the
	// abstraction and for backends that can honor it (e.g. a Managed Agents API
	// backend), but is NOT rendered into a `.claude/agents/` file today.
	MCPServers []string
}

// WriteAgentFiles generates Claude Code sub-agent definition files from a
// resolved roster. Files are written to .claude/agents/ (e.g. reviewer.md ->
// the `reviewer` sub-agent). Only exports with Enabled == true are written.
// The directory is shared with the user's own sub-agents, so cleanup is
// manifest-scoped (.ctxloom-agents-manifest) rather than a wipe — see
// agent.WriteManagedCommandFiles for the shared mechanics.
func WriteAgentFiles(workDir string, agents []AgentExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	agentsDir := filepath.Join(workDir, ".claude", "agents")

	// Reuse the manifest-scoped, traversal-safe command writer: it only needs
	// Name+Enabled to drive validation/cleanup and a render callback for bytes.
	// Carry the full AgentExport in a by-name lookup the renderer reads from.
	byName := make(map[string]AgentExport, len(agents))
	cmds := make([]agent.CommandExport, 0, len(agents))
	for _, a := range agents {
		byName[a.Name] = a
		cmds = append(cmds, agent.CommandExport{Name: a.Name, Enabled: a.Enabled})
	}

	return agent.WriteManagedCommandFiles(fs, agentsDir, ".ctxloom-agents-manifest", cmds,
		func(c agent.CommandExport) (string, []byte, error) {
			a := byName[c.Name]
			filename := strings.ReplaceAll(a.Name, "/", "-") + ".md"
			return filename, []byte(TransformToClaudeAgent(a)), nil
		})
}

// TransformToClaudeAgent renders one AgentExport as a Claude Code sub-agent
// definition: YAML frontmatter (name, description, tools, model) followed by the
// system prompt body. `name` is slugified to Claude Code's identifier shape
// (lowercase, hyphen-separated) so the file is invocable as that sub-agent.
func TransformToClaudeAgent(a AgentExport) string {
	var buf bytes.Buffer

	buf.WriteString("---\n")
	buf.WriteString("name: ")
	buf.WriteString(claudeAgentName(a.Name))
	buf.WriteString("\n")

	if a.Description != "" {
		buf.WriteString("description: ")
		buf.WriteString(agent.EscapeYAMLString(a.Description))
		buf.WriteString("\n")
	}

	// Claude Code reads `tools` as a comma-separated allowlist; omitting the key
	// inherits every tool the parent session has. An empty Tools slice therefore
	// means "inherit all" and we write no key.
	if len(a.Tools) > 0 {
		buf.WriteString("tools: ")
		buf.WriteString(strings.Join(a.Tools, ", "))
		buf.WriteString("\n")
	}

	if a.Model != "" {
		buf.WriteString("model: ")
		buf.WriteString(a.Model)
		buf.WriteString("\n")
	}

	buf.WriteString("---\n\n")
	buf.WriteString(a.SystemPrompt)
	if !strings.HasSuffix(a.SystemPrompt, "\n") {
		buf.WriteString("\n")
	}
	return buf.String()
}

// nonSlugRe matches runs of characters not allowed in a Claude Code sub-agent
// identifier (anything outside lowercase alphanumerics).
var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// claudeAgentName derives Claude Code's sub-agent identifier from a roster name:
// the leaf after any bundle prefix, lowercased, with runs of disallowed
// characters collapsed to a single hyphen and leading/trailing hyphens trimmed.
// "research/Deep Reviewer" -> "deep-reviewer".
func claudeAgentName(name string) string {
	leaf := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		leaf = name[i+1:]
	}
	slug := nonSlugRe.ReplaceAllString(strings.ToLower(leaf), "-")
	return strings.Trim(slug, "-")
}
