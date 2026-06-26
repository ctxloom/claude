# Sub-agents, looping, and HARP — design + spike

Status: **design / spike**. No behavior change yet. This documents how the
`ctxloom/claude` backend can surface Claude Code sub-agents and looping, and what
it would take to tie either to HARP-named sessions or to fold sub-agent summaries
into normalized history.

## 1. Background — the seams that exist today

The backend exposes Claude Code to ctxloom through two data surfaces, and any
integration touches one or both:

1. **Transcript reader** — `ClaudeSessionHistory.parseEntries` (`capabilities.go:190`),
   post-hoc from `~/.claude/projects/<hash>/<session>.jsonl`.
2. **Live stream** — `mapStreamJSONEvent` (`chat_stream.go:71`), real-time from
   `claude -p --input-format stream-json --output-format stream-json`
   (`chat_run.go:139`).

Both normalize into `agent.SessionEntry` (`shared/agent/backend.go:150`).

### HARP

`shared/harp` mints "Human Appropriate Random Phraselets" — `swift-amber-falcon`
(full) and adj-noun shorts. `shared/harpmarker` writes a **namespaced,
self-closing marker** into a transcript at session start:

```
<ctxloom name="plump-loose-sash" kind="harp" />
```

It is injected through the SessionStart hook's `additionalContext` path
(`hooks_wire.go:57-87`) and recovered with `harpmarker.Scan`, which decodes
through Claude's nested JSON escaping. ctxloom keys session directories by harp
(`~/.ctxloom/sessions/<harp>/…`, see `shared/agent/backend.go:138`,
`PlanFile`). The marker's `kind=` attribute is **deliberately extensible** — the
package doc anticipates other point metadata (`kind="resumed-from"`, etc.).

Ordering and cross-agent provenance are resolved by ctxloom from its **session
index**, not by the agent (`capabilities.go:339-342`, `backend.go:110-115`). The
agent only locates and materializes a given session.

### Looping

Two existing primitives:

- `shared/tasks` — a task can be `Deferred` with a non-empty **`Trigger`** (the
  named wake-up condition; `task.go:56-61`, `ValidateStatusTrigger`). Tasks are
  harp-keyed and stamp the creating session's harp as `OriginSession`.
- `StructuredChat.Chat` (`chat_run.go:42`) is itself a persistent multi-turn
  `in`/`out` channel loop.

## 2. Where sub-agents are today: dropped, summaries kept anonymously

`parseEntries` discards every sub-agent line wholesale:

```go
// capabilities.go:195
if raw.IsSidechain {
    return nil, nil   // "session reflects only its main thread"
}
```

But the sub-agent **summary already survives** — just anonymously. A `Task`-tool
sub-agent's final message returns to the parent as the `tool_result` of the
parent's `Task` `tool_use`, and that pair is **main-thread**, so it is already
parsed into two entries:

- a `tool_use` entry, `ToolName="Task"`, prompt + `subagent_type` in `ToolInput`;
- a `tool_result` entry carrying the summary, but with `ToolName` **empty**
  (`chat_stream.go:127-128` notes the block has `tool_use_id` but no name).

So the gap is not "summaries are lost" — it's that they are **unlabeled and
unlinked**. There is no field on `agent.SessionEntry` for sub-agent identity or
parent linkage.

## 3. Spike — can we stamp a harp into a sub-agent's sidechain?

**Finding: not with the current hook surface.** This is the load-bearing result.

- The marker is injected via **SessionStart** (`hooks_wire.go:71` —
  `source: startup|resume|clear|compact`). Claude Code fires SessionStart **once
  per top-level session**; a `Task` sub-agent inherits the parent session and
  does **not** get its own SessionStart event. So the existing injection path
  cannot reach a sidechain.
- **PreToolUse** (`hooks_wire.go:23-55`) sees `tool_name`/`tool_input` and can
  deny or inject context, but only into the **parent** thread — it has no seam to
  write into the *sub-agent's own* transcript. Appending a marker instruction to
  the `Task` prompt is unreliable: sub-agent output is summarized, so an emitted
  marker may not survive into the parent-visible result.

**Reframe (tractable B):** don't inject into the sidechain — **derive and map**.
Every stream-json event carries a top-level `parent_tool_use_id` (confirmed in
`testdata/streamjson/*.json`); a sub-agent's events arrive with
`parent_tool_use_id` = the parent `Task` `tool_use` id. Mint a harp per sub-agent
**host-side**, keyed by that id, and record the parent↔child relation in
ctxloom's session index. This keeps identity where the architecture already puts
it (the index, not the agent transcript) and avoids the unsupported injection.

> Live-stream gap worth flagging independently: `mapStreamJSONEvent`
> (`chat_stream.go:71`) ignores `parent_tool_use_id` entirely, so a sub-agent's
> **interior** events (its own tool calls) are currently flattened into the main
> stream as if they were main-thread. Whatever we build, the stream path should
> branch on `parent_tool_use_id != null`.

## 4. Wire shapes (from `testdata/streamjson`)

Every event has top-level `parent_tool_use_id`, `uuid`, `session_id`:

```jsonc
// assistant tool_use (parent thread): parent_tool_use_id: null
{"type":"assistant","message":{"content":[{"type":"tool_use",
  "id":"toolu_01Vu…","name":"Bash","input":{…},"caller":{"type":"direct"}}]},
  "parent_tool_use_id":null,"uuid":"…","session_id":"…"}

// tool_result (parent thread): links back via tool_use_id
{"type":"user","message":{"content":[{"tool_use_id":"toolu_01Vu…",
  "type":"tool_result","content":"…","is_error":false}]},
  "parent_tool_use_id":null,…}
```

For a sub-agent, the `Task` `tool_use` lives on the parent thread; the
sub-agent's own events come back with `parent_tool_use_id` set to that `Task`
id. Transcript-side, sub-agent lines carry `isSidechain:true` and a `parentUuid`
chain. The cross-path correlation key is the `Task` tool_use id ↔ the
`tool_result` with matching `tool_use_id`.

## 5. Options

### Option A — Sub-agent summaries into history (smallest, in-repo)

Stop discarding sidechains blindly; **correlate** instead. Keep sidechain bodies
out of the main thread, but tag the main-thread `Task` `tool_use`/`tool_result`
pair with sub-agent identity and link them by `tool_use_id`.

Needs:
- One additive field on `agent.SessionEntry` — e.g. `SubagentType string` and/or
  `ParentToolUseID string` (shared module).
- `parseEntries` / `mapToolResults`: set `ToolName` on the `Task` result and
  carry the link.

Result: history shows "sub-agent *X* was asked *Y*, returned *Z*" as a
first-class, attributable turn. No new directories, no markers. This field is
also the prerequisite for B and C.

### Option B — HARP-named sub-agents as child sessions (richest, spans repos)

Per §3, **derive-and-map** rather than inject: mint a harp per sub-agent keyed by
`parent_tool_use_id`, fold each sub-agent into ctxloom's session index as a child
session with its own `~/.ctxloom/sessions/<harp>/` dir and plans, and reference
it from the parent. Could reuse the marker vocabulary as `kind="subagent"` /
`kind="parent"` for any transcript-visible breadcrumbs, but the **index** is the
authority. Requires changes in ctxloom proper (out of this repo) plus the §5.A
schema field. Bigger; do after A.

### Option C — Looping via `tasks.Trigger` (composes on A/B)

Model a loop iteration as a `Deferred` task whose `Trigger` is the recurrence
condition (`task.go:56-61`). On wake it spawns a (harp-named, per B) sub-agent;
the iteration's summary folds back as a task-log entry or a history
`SessionEntry`. This is the looping leg — it layers on A/B, it does not compete.

## 6. Recommendation / sequencing

1. **A first** — contained in this repo (plus one additive shared field),
   unblocks the visible win, and yields the linkage field B and C both need.
2. **B** when ready to touch ctxloom's session index, using derive-and-map.
3. **C** as the looping wrapper on top.

Independent of the above, fix the `chat_stream.go` `parent_tool_use_id` gap so
live sub-agent interior events aren't misattributed.

## 7. Open questions

- `SessionEntry` shape: minimal `ParentToolUseID` + `SubagentType`, or a nested
  `Subagent *SubagentRef`? Affects every backend that embeds the type.
- Do we want sub-agent **interior** turns in history at all, or only the
  summary? A keeps only the summary; full interiors are a strictly larger change.
- Harp granularity for sub-agents: full tri-word vs. adj-noun short (tasks use
  short; sub-agents per session may be few — short likely fine).
- Cross-path consistency: the live stream keys on `parent_tool_use_id`; the
  transcript keys on `isSidechain` + `parentUuid`. Confirm both resolve to the
  same `Task` tool_use id so A/B behave identically live vs. post-hoc.
