# Multi-agent roster — rollout plan (remaining needs)

The Claude Code materialization landed in this repo (`agentfiles.go`,
`agentfiles_test.go`; see `multi-agent-bundle-component.md`). It is an isolated,
tested building block — **nothing invokes it end-to-end yet**. This plan
enumerates the remaining work to turn it into a usable feature: declare a roster
in a bundle, bind each agent to a profile, and have it materialize per backend.

Status legend: ☐ todo · ◐ partial · ☑ done

## Phase 0 — this repo (`ctxloom/claude`)

- ☑ `AgentExport` spec, `WriteAgentFiles`, `TransformToClaudeAgent`,
  `ClaudeAgents.RegisterRoster` (manifest-scoped, traversal-safe; preserves
  user-authored sub-agents).
- ☑ Tests under `-race`; mutation-checked (`just mutation`, gremlins v0.6.0) —
  agentfiles.go: all killable mutants killed.
- ☐ **Verify against the real `claude` CLI** that a generated
  `.claude/agents/<name>.md` is accepted and routable. Tests currently assert
  *our* expected frontmatter shape (`name`/`description`/`tools`/`model` + body),
  derived from docs, not the CLI's actual parser. Acceptance: spawn `claude`,
  confirm the sub-agent is listed/invocable; correct frontmatter if it diverges.
- ☐ Once `shared` lands the types (Phase 1): delete the local `AgentExport`,
  import `agent.AgentExport`, and implement the `ContentRoster` capability on
  the backend; register it in `NewClaudeCode` → `InitLaunch` alongside
  `ClaudeSkills`.

## Phase 1 — `ctxloom/shared`

Mirror the existing command-export plumbing exactly (`CommandExport` /
`ManagedConfig.Prompts` / `ContentSkills`).

- ☐ Promote `AgentExport` into `shared/agent` (next to `CommandExport` in
  `agent/commandfiles.go` or a new `agent/agentexport.go`). Same field set:
  `Name, Profile, Enabled, Description, SystemPrompt, Tools, Model, MCPServers`.
- ☐ Add `ManagedConfig.Roster []AgentExport` (`agent/backend.go`, next to
  `Prompts []CommandExport`).
- ☐ Add a `ContentRoster` capability interface in `agent/launch_backend.go`
  (parallel to `ContentSkills`):
  `RegisterRoster(workDir string, roster []AgentExport) error`.
- ☐ Wire `LaunchBackend.InitLaunch`/`Setup` to call the roster registrar when the
  backend implements `ContentRoster` and `req.Managed.Roster` is non-empty —
  same call site/sequence as `skills.RegisterFromContent(workDir, m.Prompts)`.
- ☐ Tests: registrar invoked with the resolved roster; no-op when empty.

Blast radius: additive only (new field, new optional interface). No existing
backend changes behavior until it implements `ContentRoster`.

## Phase 2 — `ctxloom` (host)

- ☐ **Bundle component schema** for a roster, e.g.
  `roster { agent <name> { profile = "<profile>" [description = "…"] } }`.
  Decide whether `description` (the coordinator's delegation hint) is authored on
  the roster entry or pulled from the profile.
- ☐ **Profile → config resolution**: resolve each agent's bound profile to
  `Model`, `Tools`, `MCPServers`, and system prompt. Reuse the same profile
  resolver the rest of ctxloom uses (no parallel config vocabulary).
- ☐ **Wiring boundary**: map each resolved roster entry to an `AgentExport` for
  the target backend (resolving per-backend enablement), exactly as bundle
  content already maps to `CommandExport`. Populate `ManagedConfig.Roster`.
- ☐ Surface the roster in the session-index / provenance if a roster agent's
  identity should be addressable (ties into the earlier HARP/sub-agent work in
  `subagents-looping-integration.md` — out of scope for the first cut).

## Phase 3 — optional second backend (proves the abstraction)

- ☐ A **Managed Agents API** backend consuming the *same* `AgentExport`, POSTing
  `model`/`system`/`tools`/`mcp_servers` per agent and a coordinator referencing
  the roster. This is where `AgentExport.MCPServers` (carried but not rendered by
  the CLI backend, since CLI sub-agents can't scope MCP) is actually honored.

## Tooling / CI

- ☑ `just mutation` target (gremlins v0.6.0, pinned).
- ☐ Optional CI gate: fail if mutation efficacy on changed files drops below a
  threshold (the repo currently has no CI config checked in).

## Open questions

1. **Name collisions**: a roster agent name may collide with a user-authored
   `.claude/agents/*.md`. Manifest cleanup only removes ctxloom-written files, so
   we never delete the user's — but two files with the same `name:` frontmatter
   is a CLI-level conflict. Decide: namespace ctxloom agents, or warn on collision.
2. **`description` is load-bearing**: Claude Code auto-routes to a sub-agent by
   its `description`. A weak description = the coordinator never delegates.
   Should the profile mandate one?
3. **Model alias mapping**: profiles may carry full model ids; Claude Code
   sub-agent `model:` accepts aliases (`opus`/`sonnet`/`haiku`) or ids. Confirm
   pass-through vs. needing a map.
4. **MCP scoping gap**: the CLI can't scope MCP per sub-agent. Acceptable for v1
   (session-global MCP), or block until the API backend exists?
