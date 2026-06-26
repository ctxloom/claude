# Multi-agent roster as a ctxloom bundle component

Abstracts the [Managed Agents multi-agent model](https://platform.claude.com/docs/en/managed-agents/multi-agent)
— a coordinator plus a roster of agents, where each agent has "its own
configuration (model, system prompt, tools, MCP servers, and skills)" — into a
ctxloom **bundle component**, where **each agent takes a profile**.

## The mapping

The managed-agents model says each agent carries model + system prompt + tools +
MCP servers + skills. ctxloom already has one noun for that bundle of config: a
**profile**. So the abstraction is:

```
bundle "team" {
  roster {
    agent reviewer  { profile = "deep-review" }
    agent test-writer { profile = "tdd" }
  }
}
```

A roster lists agents; each agent **binds a ctxloom profile** that supplies its
model/tools/MCP/system-prompt. The host resolves the profile to concrete fields
— exactly as it already resolves a `CommandExport`'s `Enabled`/`Model` — and
hands each backend a resolved `AgentExport`. Backends only render + write.

## What this repo (the Claude Code backend) implements — DONE

`agentfiles.go` is the claude-code materialization, built to mirror
`commandfiles.go` one-for-one:

| commands (existing)            | agents (new)                          |
| ------------------------------ | ------------------------------------- |
| `agent.CommandExport`          | `claude.AgentExport`                  |
| `WriteCommandFiles`            | `WriteAgentFiles`                     |
| `TransformToClaudeCommand`     | `TransformToClaudeAgent`             |
| `.claude/commands/<n>.md`      | `.claude/agents/<n>.md`               |
| `.ctxloom-manifest`            | `.ctxloom-agents-manifest`            |
| `ClaudeSkills.RegisterFromContent` | `ClaudeAgents.RegisterRoster`     |

- `AgentExport` is the agent-agnostic roster-member spec. It carries `Profile`
  (the bound profile, for provenance) plus the host-resolved `Description`,
  `SystemPrompt`, `Tools`, `Model`, and `MCPServers`.
- `WriteAgentFiles` reuses the shared, traversal-safe, manifest-scoped writer
  (`agent.WriteManagedCommandFiles`), so `.claude/agents/` — shared with the
  user's own sub-agents — is never wiped wholesale and user-authored sub-agents
  are preserved (tested).
- `TransformToClaudeAgent` renders the Claude Code sub-agent file: YAML
  frontmatter (`name`, `description`, `tools`, `model`) + system-prompt body,
  with `name` slugified to Claude Code's identifier shape.

Covered by `agentfiles_test.go`: rendering, enablement, nested-name flattening,
manifest cleanup, user-file preservation, traversal-name rejection, empty-roster
teardown, and the MCP-carried-not-rendered limitation.

## Known limitation (encoded, not hidden)

`AgentExport.MCPServers` exists because MCP is **agent-scoped** in the
managed-agents model. Claude Code's CLI sub-agents cannot scope MCP per
sub-agent (MCP is session-global there), so the field is carried for the
abstraction and for backends that can honor it, but is **not** rendered into a
`.claude/agents/` file. A future Managed Agents API backend would consume the
same `AgentExport` and POST `mcp_servers` per agent.

## What remains — cross-repo (out of this module)

1. **`ctxloom/shared`**: promote `AgentExport` to `shared/agent` (next to
   `CommandExport`) and add `ManagedConfig.Roster []AgentExport` (next to
   `Prompts`), plus a `ContentRoster` capability interface
   (`RegisterRoster(workDir, []AgentExport) error`) that `LaunchBackend.Setup`
   invokes. Until then `ClaudeAgents.RegisterRoster` is callable directly and the
   local `AgentExport` is a drop-in for the shared type.
2. **`ctxloom`**: the bundle-component schema (`roster { agent <name> { profile
   = … } }`), and the wiring boundary that resolves each agent's bound profile to
   model/tools/MCP and maps it to an `AgentExport` for the target backend
   (mirroring how bundle content already maps to `CommandExport`).

## Why a profile per agent (not inline config)

Specialization is the whole point of the multi-agent model (a security agent, a
docs agent). Profiles are how ctxloom already names a reusable config surface, so
binding an agent to a profile means a roster reuses the same model/tooling
policy the rest of ctxloom uses — no parallel, drifting config vocabulary.
