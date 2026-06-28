package claude

import (
	"encoding/json"
	"sort"

	"github.com/ctxloom/shared/agent"
)

// This file maps claude-code's `--output-format stream-json` events to ctxloom's
// backend-agnostic chat turns. All claude-specific wire knowledge lives here (per
// the polymorphic design): shared/agent and ctxloom only ever see agent.ChatEvent.

// --- stream-json wire shapes (subset we consume) ---

type sjEvent struct {
	Type    string     `json:"type"`
	Subtype string     `json:"subtype"`
	Message *sjMessage `json:"message"`
	// result fields
	Usage      *sjUsage              `json:"usage"`
	ModelUsage map[string]sjModelUse `json:"modelUsage"`
	TotalCost  float64               `json:"total_cost_usd"`
	DurationMs int                   `json:"duration_ms"`
	NumTurns   int                   `json:"num_turns"`
	StopReason string                `json:"stop_reason"`
	// system/init fields
	Model          string  `json:"model"`
	PermissionMode string  `json:"permissionMode"`
	MCPServers     []sjMCP `json:"mcp_servers"`
}

type sjMessage struct {
	Content json.RawMessage `json:"content"` // string OR array of blocks
}

// The content-block shape (text/thinking/tool_use/tool_result) is identical on
// the wire whether it arrives in a live `--output-format stream-json` event or in
// a persisted transcript line, so this file reuses the single claudeBlock type and
// the single claudeBlockText flattener defined in capabilities.go rather than
// carrying its own copy (kept in lockstep — see claude-code-01-003).

type sjUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type sjModelUse struct {
	OutputTokens    int     `json:"outputTokens"`
	ContextWindow   int     `json:"contextWindow"`
	MaxOutputTokens int     `json:"maxOutputTokens"`
	CostUSD         float64 `json:"costUSD"`
}

type sjMCP struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// mapStreamJSONEvent normalizes one stream-json line into 0..N ChatEvents. An
// assistant message holds an array of content blocks, so one event can yield
// several entries. Unknown/irrelevant events (hook_*, thinking_tokens,
// rate_limit_event, malformed JSON, a future event type) return nil — the stream
// must never crash on something we don't model.
func mapStreamJSONEvent(raw []byte) []agent.ChatEvent {
	var e sjEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil
	}
	switch e.Type {
	case "assistant":
		return mapAssistantBlocks(e.Message)
	case "user":
		return mapToolResults(e.Message)
	case "result":
		return []agent.ChatEvent{{Complete: resultToTurnMeta(&e)}}
	case "system":
		if e.Subtype == "init" {
			return []agent.ChatEvent{{Session: initToSessionInfo(&e)}}
		}
		return nil
	default:
		return nil
	}
}

// mapAssistantBlocks turns an assistant message's content blocks into entries:
// text → assistant, thinking → thinking, tool_use → tool_use; other blocks are
// dropped.
func mapAssistantBlocks(m *sjMessage) []agent.ChatEvent {
	if m == nil {
		return nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		// content may be a bare string (rare for assistant).
		var s string
		if json.Unmarshal(m.Content, &s) == nil && s != "" {
			return []agent.ChatEvent{{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: s}}}
		}
		return nil
	}
	var out []agent.ChatEvent
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: b.Text}})
			}
		case "thinking":
			// Emit a thinking marker even when the text is blank. NOTE: claude-code
			// intentionally strips the reasoning text from its `-p --output-format
			// stream-json` output — the block arrives as {type:"thinking",
			// thinking:"", signature:"…"}, signature only, and no thinking_delta
			// events are emitted even with --include-partial-messages. The signature
			// is kept for multi-turn API replay; the prose is withheld from
			// programmatic consumers by design (the TUI shows it ephemerally instead).
			// So b.Thinking is empty in practice, but we still surface the block as a
			// content-less entry so a live frontend can show that the model reasoned
			// this turn. There are no timestamps in stream-json — only the turn-level
			// timing carried by the result/Complete event. Content carries the prose
			// unchanged if a future build (or a direct-API backend) ever provides it.
			out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: b.Thinking}})
		case "tool_use":
			out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{
				Type:      agent.EntryTypeToolUse,
				ToolName:  b.Name,
				ToolInput: json.RawMessage(b.Input),
			}})
		}
	}
	return out
}

// mapToolResults turns a user message's tool_result blocks into tool_result
// entries. The block carries tool_use_id but not the tool name, so ToolName is
// left empty (a later enhancement may correlate it to the prior tool_use).
func mapToolResults(m *sjMessage) []agent.ChatEvent {
	if m == nil {
		return nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil
	}
	var out []agent.ChatEvent
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{
			Type:       agent.EntryTypeToolResult,
			ToolOutput: claudeBlockText(b.Content),
			IsError:    b.IsError,
		}})
	}
	return out
}

// resultToTurnMeta extracts completion accounting. Token counts come from the
// turn-level usage; context-window / max-output / per-model limits come from the
// generating model's modelUsage entry. The `result` string is deliberately NOT
// read — content comes only from the assistant entry, never duplicated here.
func resultToTurnMeta(e *sjEvent) *agent.TurnMeta {
	tm := &agent.TurnMeta{
		CostUSD:    e.TotalCost,
		StopReason: e.StopReason,
		DurationMs: e.DurationMs,
		NumTurns:   e.NumTurns,
	}
	if e.Usage != nil {
		tm.InputTokens = e.Usage.InputTokens
		tm.OutputTokens = e.Usage.OutputTokens
		tm.CacheReadTokens = e.Usage.CacheReadInputTokens
		tm.CacheCreationTokens = e.Usage.CacheCreationInputTokens
	}
	model, mu := pickGeneratingModel(e.ModelUsage)
	tm.Model = model
	tm.ContextWindow = mu.ContextWindow
	tm.MaxOutputTokens = mu.MaxOutputTokens
	return tm
}

// pickGeneratingModel chooses the model that produced the result — the
// modelUsage entry with the most output tokens (ties broken on sorted id for
// determinism), matching the provenance rule used elsewhere in this backend.
func pickGeneratingModel(m map[string]sjModelUse) (string, sjModelUse) {
	return pickByMaxOutput(m, func(u sjModelUse) int { return u.OutputTokens })
}

// pickByMaxOutput returns the key of m whose out(value) is greatest, breaking
// ties on the lexicographically smallest key so the choice is deterministic; the
// zero key ("") is returned for an empty map. This is the single provenance rule
// both result parsers use to name the generating model: the CLI may route a large
// read through an ancillary fast model (high input, tiny output) while the
// requested model does the real generation, so output — not input — marks the
// working model. parseClaudeJSONResult (JSON envelope) and pickGeneratingModel
// (stream-json modelUsage) both build on it.
func pickByMaxOutput[T any](m map[string]T, out func(T) int) (string, T) {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var best string
	var bestVal T
	bestTokens := -1
	for _, id := range ids {
		if n := out(m[id]); n > bestTokens {
			best, bestVal, bestTokens = id, m[id], n
		}
	}
	return best, bestVal
}

func initToSessionInfo(e *sjEvent) *agent.ChatSessionInfo {
	s := &agent.ChatSessionInfo{Model: e.Model, PermissionMode: e.PermissionMode}
	for _, m := range e.MCPServers {
		s.MCPServers = append(s.MCPServers, agent.MCPStatus{Name: m.Name, Status: m.Status})
	}
	return s
}
