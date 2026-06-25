package claude

import (
	"encoding/json"
	"sort"
	"strings"

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

type sjBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"` // tool_result payload: string OR blocks
	IsError bool            `json:"is_error"`
}

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
// text → assistant, tool_use → tool_use; thinking and other blocks are dropped.
func mapAssistantBlocks(m *sjMessage) []agent.ChatEvent {
	if m == nil {
		return nil
	}
	var blocks []sjBlock
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
	var blocks []sjBlock
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
			ToolOutput: textOfContent(b.Content),
			IsError:    b.IsError,
		}})
	}
	return out
}

// textOfContent flattens a tool_result content payload (a JSON string, or an
// array of {type:text,text} blocks) to plain text.
func textOfContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []sjBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" {
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return string(raw)
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
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	best, bestTokens := "", -1
	var bu sjModelUse
	for _, id := range ids {
		if m[id].OutputTokens > bestTokens {
			best, bu, bestTokens = id, m[id], m[id].OutputTokens
		}
	}
	return best, bu
}

func initToSessionInfo(e *sjEvent) *agent.ChatSessionInfo {
	s := &agent.ChatSessionInfo{Model: e.Model, PermissionMode: e.PermissionMode}
	for _, m := range e.MCPServers {
		s.MCPServers = append(s.MCPServers, agent.MCPStatus{Name: m.Name, Status: m.Status})
	}
	return s
}
