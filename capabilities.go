package claude

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
)

// ClaudeSkills registers slash commands for Claude Code.
type ClaudeSkills struct{}

// RegisterFromContent writes slash commands from host-resolved command exports.
// The host maps bundle content (with claude-code enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *ClaudeSkills) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// ClaudeSessionHistory implements SessionHistory for Claude Code.
// Reads from ~/.claude/projects/<hash>/session.jsonl. The embedded
// agent.SessionStore carries the afero fs + homeDir injection points used by
// tests and the shared transcript parse loop.
type ClaudeSessionHistory struct {
	backend *ClaudeCode
	agent.SessionStore
}

// ClaudeSessionHistoryOption configures ClaudeSessionHistory.
type ClaudeSessionHistoryOption func(*ClaudeSessionHistory)

// WithClaudeSessionFS sets a custom filesystem for testing.
func WithClaudeSessionFS(fs afero.Fs) ClaudeSessionHistoryOption {
	return func(h *ClaudeSessionHistory) {
		h.FS = fs
	}
}

// WithClaudeSessionHomeDir sets a custom home directory for testing.
func WithClaudeSessionHomeDir(dir string) ClaudeSessionHistoryOption {
	return func(h *ClaudeSessionHistory) {
		h.HomeDir = dir
	}
}

// NewClaudeSessionHistory creates a new Claude session history handler.
func NewClaudeSessionHistory(backend *ClaudeCode, opts ...ClaudeSessionHistoryOption) *ClaudeSessionHistory {
	h := &ClaudeSessionHistory{
		backend:      backend,
		SessionStore: agent.NewSessionStore(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *ClaudeSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	sessions, err := h.ListSessions(workDir)
	return agent.MostRecentSession(sessions, err, func(m agent.SessionMeta) (*agent.Session, error) {
		return h.GetSession(workDir, m.ID)
	})
}

// ListSessions returns available session metadata, most recent first.
func (h *ClaudeSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	// Look for session files in the project directory
	entries, err := afero.ReadDir(agent.GetFS(h.FS), projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read project directory: %w", err)
	}

	var sessions []agent.SessionMeta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessions = append(sessions, agent.SessionMeta{
			ID:        strings.TrimSuffix(entry.Name(), ".jsonl"),
			StartTime: entry.ModTime(), // Approximate - would need to read file for exact
			Path:      filepath.Join(projectDir, entry.Name()),
		})
	}

	agent.SortSessionsMostRecentFirst(sessions)
	return sessions, nil
}

// GetSession returns a specific session by ID.
func (h *ClaudeSessionHistory) GetSession(workDir string, sessionID string) (*agent.Session, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(projectDir, sessionID+".jsonl")
	return h.parseSessionFile(sessionPath)
}

// GetSessionByPath returns a session by its transcript file path.
func (h *ClaudeSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return h.parseSessionFile(path)
}

// findProjectDir finds the Claude project directory for the given workDir.
// Claude Code converts paths by replacing / with - and prefixing with -.
// Example: /home/user/project -> -home-user-project
func (h *ClaudeSessionHistory) findProjectDir(workDir string) (string, error) {
	homeDir, err := h.ResolveHomeDir()
	if err != nil {
		return "", err
	}

	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Claude Code converts paths: /home/user/project -> -home-user-project
	projectName := strings.ReplaceAll(absPath, string(filepath.Separator), "-")

	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)
	if _, err := agent.GetFS(h.FS).Stat(projectDir); err != nil {
		return "", fmt.Errorf("project directory not found: %s", projectDir)
	}

	return projectDir, nil
}

// parseSessionFile reads and parses a Claude session JSONL file via the
// shared SessionStore loop; malformed lines are skipped.
func (h *ClaudeSessionHistory) parseSessionFile(path string) (*agent.Session, error) {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return h.ParseSessionFile(path, id, func(line []byte) []agent.SessionEntry {
		entries, err := h.parseEntries(line)
		if err != nil {
			return nil // Skip malformed entries
		}
		return entries
	})
}

// claudeEntry represents a raw top-level entry from Claude's session.jsonl.
type claudeEntry struct {
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
	// Legacy flat tool fields (older transcript schema, kept for back-compat).
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Output  string          `json:"output"`
	IsError bool            `json:"is_error"`
}

// claudeMessage is the {role, content} object on user/assistant entries.
// Content is either a JSON string (legacy) or an array of typed blocks.
type claudeMessage struct {
	Content json.RawMessage `json:"content"`
}

// claudeBlock is one content block in the modern array schema: text/thinking
// for prose, tool_use for calls, tool_result for outputs.
type claudeBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// parseEntries converts one Claude JSONL line into zero or more normalized
// SessionEntries. The modern schema nests typed blocks under message.content,
// so one assistant line can yield a text entry plus a tool-call entry per
// tool_use block, and a user line can yield tool-result entries. Sidechain
// (sub-agent) lines are skipped so a session reflects only its main thread.
func (h *ClaudeSessionHistory) parseEntries(line []byte) ([]agent.SessionEntry, error) {
	var raw claudeEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	if raw.IsSidechain {
		return nil, nil
	}
	ts := parseClaudeTimestamp(raw.Timestamp)
	switch raw.Type {
	case "user", "human":
		return claudeMessageEntries(raw.Message, ts, agent.EntryTypeUser), nil
	case "assistant":
		return claudeMessageEntries(raw.Message, ts, agent.EntryTypeAssistant), nil
	case "system":
		if c := claudeFirstText(raw.Message); c != "" {
			return []agent.SessionEntry{{Timestamp: ts, Type: agent.EntryTypeSystem, Content: c}}, nil
		}
	case "tool_use": // legacy flat schema
		return []agent.SessionEntry{{Timestamp: ts, Type: agent.EntryTypeToolUse, ToolName: raw.Name, ToolInput: raw.Input}}, nil
	case "tool_result": // legacy flat schema
		return []agent.SessionEntry{{Timestamp: ts, Type: agent.EntryTypeToolResult, ToolName: raw.Name, ToolOutput: raw.Output, IsError: raw.IsError}}, nil
	}
	return nil, nil
}

// parseClaudeTimestamp parses an RFC3339 timestamp, returning the zero time for
// an empty or unparseable value.
func parseClaudeTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// claudeBlocks decodes a message's content into blocks. content may be a plain
// JSON string (legacy — returned as a single text block) or an array of blocks
// (modern schema).
func claudeBlocks(message json.RawMessage) []claudeBlock {
	var msg claudeMessage
	if len(message) == 0 || json.Unmarshal(message, &msg) != nil || len(msg.Content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(msg.Content, &s) == nil {
		return []claudeBlock{{Type: "text", Text: s}}
	}
	var blocks []claudeBlock
	if json.Unmarshal(msg.Content, &blocks) == nil {
		return blocks
	}
	return nil
}

// claudeMessageEntries decomposes one user/assistant message's content blocks
// into normalized entries, preserving order: prose text becomes a proseType
// entry, tool_use becomes a ToolUse entry, tool_result becomes a ToolResult
// entry. thinking and other block types are intentionally dropped.
func claudeMessageEntries(message json.RawMessage, ts time.Time, proseType agent.SessionEntryType) []agent.SessionEntry {
	var out []agent.SessionEntry
	var text strings.Builder
	flushText := func() {
		if text.Len() > 0 {
			out = append(out, agent.SessionEntry{Timestamp: ts, Type: proseType, Content: text.String()})
			text.Reset()
		}
	}
	for _, b := range claudeBlocks(message) {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(b.Text)
			}
		case "tool_use":
			flushText()
			out = append(out, agent.SessionEntry{Timestamp: ts, Type: agent.EntryTypeToolUse, ToolName: b.Name, ToolInput: b.Input})
		case "tool_result":
			flushText()
			out = append(out, agent.SessionEntry{Timestamp: ts, Type: agent.EntryTypeToolResult, ToolOutput: claudeBlockText(b.Content), IsError: b.IsError})
		}
	}
	flushText()
	return out
}

// claudeBlockText flattens a tool_result block's content (a string, or an array
// of {type:"text", text}) to a plain string.
func claudeBlockText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []claudeBlock
	if json.Unmarshal(content, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

// claudeFirstText returns the first text block's text from a message (used for
// short system entries).
func claudeFirstText(message json.RawMessage) string {
	for _, b := range claudeBlocks(message) {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// TranscriptPathFromHook computes the transcript path from hook input.
// For Claude, we compute the path from sessionID + workDir. Honors the
// h.homeDir override when set so tests can pin the expected path
// without depending on $HOME.
func (h *ClaudeSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	if sessionID == "" {
		return ""
	}
	homeDir, err := h.ResolveHomeDir()
	if err != nil {
		return ""
	}
	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	projectName := strings.ReplaceAll(absPath, string(filepath.Separator), "-")
	return filepath.Join(homeDir, ".claude", "projects", projectName, sessionID+".jsonl")
}

// "Which session is previous" now lives in ctxloom
// (operations.ResolvePreviousSession), resolved from the session index rather
// than by scanning transcripts for harp markers here. This reader only locates,
// reassembles, and translates a given session.
