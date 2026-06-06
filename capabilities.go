package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
)

// ClaudeLifecycle implements LifecycleHandler for Claude Code using hooks.
// Embeds BaseLifecycle for shared implementation.
type ClaudeLifecycle struct {
	*BaseLifecycle
	backend *ClaudeCode
}

// NewClaudeLifecycle creates a new Claude lifecycle handler.
func NewClaudeLifecycle(backend *ClaudeCode) *ClaudeLifecycle {
	return &ClaudeLifecycle{
		BaseLifecycle: NewBaseLifecycle("claude-code", backend.writeSettings),
		backend:       backend,
	}
}

// ClaudeMCPManager implements MCPManager for Claude Code.
// Embeds BaseMCPManager for shared implementation.
type ClaudeMCPManager struct {
	*BaseMCPManager
	backend *ClaudeCode
}

// NewClaudeMCPManager creates a new Claude MCP manager.
func NewClaudeMCPManager(backend *ClaudeCode) *ClaudeMCPManager {
	return &ClaudeMCPManager{
		BaseMCPManager: NewBaseMCPManager("claude-code", backend.writeSettings),
		backend:        backend,
	}
}

// ClaudeSkills implements SkillRegistry for Claude Code using slash commands.
type ClaudeSkills struct {
	backend *ClaudeCode
}

// Register adds a skill as a Claude Code slash command.
func (s *ClaudeSkills) Register(workDir string, skill Skill) error {
	return WriteCommandFiles(workDir, []agent.CommandExport{skillExport(skill)})
}

// RegisterAll adds multiple skills as Claude Code slash commands.
func (s *ClaudeSkills) RegisterAll(workDir string, skills []Skill) error {
	cmds := make([]agent.CommandExport, 0, len(skills))
	for _, skill := range skills {
		cmds = append(cmds, skillExport(skill))
	}
	return WriteCommandFiles(workDir, cmds)
}

// RegisterFromContent writes slash commands from host-resolved command exports.
// The host maps bundle content (with claude-code enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *ClaudeSkills) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// skillExport maps a Skill to an enabled command export.
func skillExport(skill Skill) agent.CommandExport {
	return agent.CommandExport{
		Name:        skill.Name,
		Content:     skill.Content,
		Enabled:     true,
		Description: skill.Description,
	}
}

// Clear removes all ctxloom-managed skills using the manifest.
func (s *ClaudeSkills) Clear(workDir string) error {
	commandsDir := filepath.Join(workDir, ".claude", "commands")
	manifestPath := filepath.Join(commandsDir, ".ctxloom-manifest")

	// Clean up old subdirectory style (migration)
	_ = os.RemoveAll(filepath.Join(commandsDir, "ctxloom"))

	// Read manifest and remove tracked files
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, name := range strings.Split(string(data), "\n") {
		if name = strings.TrimSpace(name); name != "" {
			_ = os.Remove(filepath.Join(commandsDir, name))
		}
	}

	return os.Remove(manifestPath)
}

// List returns registered skill names from the manifest.
func (s *ClaudeSkills) List(workDir string) ([]string, error) {
	manifestPath := filepath.Join(workDir, ".claude", "commands", ".ctxloom-manifest")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			// Remove .md extension
			name = strings.TrimSuffix(name, ".md")
			names = append(names, name)
		}
	}
	return names, nil
}

// ClaudeContext implements ContextProvider for Claude Code using file + hook.
// Embeds BaseContextProvider for shared implementation.
type ClaudeContext struct {
	*BaseContextProvider
	backend *ClaudeCode
}

// NewClaudeContext creates a new Claude context provider.
func NewClaudeContext(backend *ClaudeCode) *ClaudeContext {
	return &ClaudeContext{
		BaseContextProvider: NewBaseContextProvider(),
		backend:             backend,
	}
}

// ClaudeSessionHistory implements SessionHistory for Claude Code.
// Reads from ~/.claude/projects/<hash>/session.jsonl
type ClaudeSessionHistory struct {
	backend *ClaudeCode
	fs      afero.Fs
	homeDir string // Override home directory for testing
}

// ClaudeSessionHistoryOption configures ClaudeSessionHistory.
type ClaudeSessionHistoryOption func(*ClaudeSessionHistory)

// WithClaudeSessionFS sets a custom filesystem for testing.
func WithClaudeSessionFS(fs afero.Fs) ClaudeSessionHistoryOption {
	return func(h *ClaudeSessionHistory) {
		h.fs = fs
	}
}

// WithClaudeSessionHomeDir sets a custom home directory for testing.
func WithClaudeSessionHomeDir(dir string) ClaudeSessionHistoryOption {
	return func(h *ClaudeSessionHistory) {
		h.homeDir = dir
	}
}

// NewClaudeSessionHistory creates a new Claude session history handler.
func NewClaudeSessionHistory(backend *ClaudeCode, opts ...ClaudeSessionHistoryOption) *ClaudeSessionHistory {
	h := &ClaudeSessionHistory{
		backend: backend,
		fs:      afero.NewOsFs(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// GetCurrentSession returns the current/most recent session transcript.
func (h *ClaudeSessionHistory) GetCurrentSession(workDir string) (*Session, error) {
	sessions, err := h.ListSessions(workDir)
	if err != nil {
		return nil, err
	}

	if len(sessions) == 0 {
		return nil, fmt.Errorf("no sessions found")
	}

	// Return most recent (ListSessions is sorted by time descending)
	return h.GetSession(workDir, sessions[0].ID)
}

// ListSessions returns available session metadata.
func (h *ClaudeSessionHistory) ListSessions(workDir string) ([]SessionMeta, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	// Look for session files in the project directory
	entries, err := afero.ReadDir(h.fs, projectDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read project directory: %w", err)
	}

	var sessions []SessionMeta
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessions = append(sessions, SessionMeta{
			ID:        strings.TrimSuffix(entry.Name(), ".jsonl"),
			StartTime: entry.ModTime(), // Approximate - would need to read file for exact
			Path:      filepath.Join(projectDir, entry.Name()),
		})
	}

	// Sort by time, most recent first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	return sessions, nil
}

// GetSession returns a specific session by ID.
func (h *ClaudeSessionHistory) GetSession(workDir string, sessionID string) (*Session, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return nil, err
	}

	sessionPath := filepath.Join(projectDir, sessionID+".jsonl")
	return h.parseSessionFile(sessionPath)
}

// GetSessionByPath returns a session by its transcript file path.
func (h *ClaudeSessionHistory) GetSessionByPath(path string) (*Session, error) {
	return h.parseSessionFile(path)
}

// findProjectDir finds the Claude project directory for the given workDir.
// Claude Code converts paths by replacing / with - and prefixing with -.
// Example: /home/user/project -> -home-user-project
func (h *ClaudeSessionHistory) findProjectDir(workDir string) (string, error) {
	homeDir := h.homeDir
	if homeDir == "" {
		var err error
		homeDir, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
	}

	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Claude Code converts paths: /home/user/project -> -home-user-project
	projectName := strings.ReplaceAll(absPath, string(filepath.Separator), "-")

	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)
	if _, err := h.fs.Stat(projectDir); err != nil {
		return "", fmt.Errorf("project directory not found: %s", projectDir)
	}

	return projectDir, nil
}

// findSessionFile finds the main session file for the workDir.
func (h *ClaudeSessionHistory) findSessionFile(workDir string) (string, error) {
	projectDir, err := h.findProjectDir(workDir)
	if err != nil {
		return "", err
	}

	// Claude Code uses session.jsonl as the main session file
	sessionPath := filepath.Join(projectDir, "session.jsonl")
	if _, err := h.fs.Stat(sessionPath); err != nil {
		return "", fmt.Errorf("session file not found: %s", sessionPath)
	}

	return sessionPath, nil
}

// parseSessionFile reads and parses a Claude session JSONL file.
func (h *ClaudeSessionHistory) parseSessionFile(path string) (*Session, error) {
	file, err := h.fs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer func() { _ = file.Close() }()

	session := &Session{
		ID:      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Entries: []SessionEntry{},
	}

	scanner := bufio.NewScanner(file)
	// Increase buffer size for potentially large tool outputs
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		entries, err := h.parseEntries(line)
		if err != nil {
			// Skip malformed entries
			continue
		}
		session.Entries = append(session.Entries, entries...)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan session file: %w", err)
	}

	// Set start/end times from entries
	if len(session.Entries) > 0 {
		session.StartTime = session.Entries[0].Timestamp
		session.EndTime = session.Entries[len(session.Entries)-1].Timestamp
	}

	return session, nil
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
func (h *ClaudeSessionHistory) parseEntries(line []byte) ([]SessionEntry, error) {
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
		return claudeMessageEntries(raw.Message, ts, EntryTypeUser), nil
	case "assistant":
		return claudeMessageEntries(raw.Message, ts, EntryTypeAssistant), nil
	case "system":
		if c := claudeFirstText(raw.Message); c != "" {
			return []SessionEntry{{Timestamp: ts, Type: EntryTypeSystem, Content: c}}, nil
		}
	case "tool_use": // legacy flat schema
		return []SessionEntry{{Timestamp: ts, Type: EntryTypeToolUse, ToolName: raw.Name, ToolInput: raw.Input}}, nil
	case "tool_result": // legacy flat schema
		return []SessionEntry{{Timestamp: ts, Type: EntryTypeToolResult, ToolName: raw.Name, ToolOutput: raw.Output, IsError: raw.IsError}}, nil
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
func claudeMessageEntries(message json.RawMessage, ts time.Time, proseType SessionEntryType) []SessionEntry {
	var out []SessionEntry
	var text strings.Builder
	flushText := func() {
		if text.Len() > 0 {
			out = append(out, SessionEntry{Timestamp: ts, Type: proseType, Content: text.String()})
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
			out = append(out, SessionEntry{Timestamp: ts, Type: EntryTypeToolUse, ToolName: b.Name, ToolInput: b.Input})
		case "tool_result":
			flushText()
			out = append(out, SessionEntry{Timestamp: ts, Type: EntryTypeToolResult, ToolOutput: claudeBlockText(b.Content), IsError: b.IsError})
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
	homeDir := h.homeDir
	if homeDir == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		homeDir = hd
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
