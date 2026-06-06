// Package claude is ctxloom's Claude Code agent: the settings/hooks writer that
// implements agent.SettingsWriter. Moved from internal/lm/backends in P0 step
// 4c; the launch backend follows in 4e.
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

// NewWriter constructs the Claude Code settings writer.
func NewWriter(o agent.SettingsOptions) agent.SettingsWriter {
	return &ClaudeCodeHookWriter{FS: o.FS, statusLineDisabled: o.StatusLineDisabled}
}

// Shims to the shared core helpers/symbols so the moved code below reads
// unchanged (transitional; inline to agent.* later).
type SettingsStatus = agent.SettingsStatus

const ctxloomBinary = agent.CtxloomBinary

var (
	ctxloomMCPArgs  = agent.CtxloomMCPArgs
	getFS           = agent.GetFS
	atomicWriteFile = agent.AtomicWriteFile
)

func warn(format string, args ...any)              { agent.Warn(format, args...) }
func computeHookHash(h wire.Hook) string           { return agent.ComputeHookHash(h) }
func computeMCPServerHash(s wire.MCPServer) string { return agent.ComputeMCPServerHash(s) }
func isCtxloomManaged(command string) bool         { return agent.IsManaged(command, "ctxloom") }

// ----- moved verbatim from internal/lm/backends (hooks.go + uninstall.go) -----
// ClaudeCodeHookWriter writes hooks to Claude Code's settings.json format.
type ClaudeCodeHookWriter struct {
	// FS is the filesystem to use. If nil, the real OS filesystem is used.
	FS afero.Fs
	// statusLineDisabled opts out of managing the ctxloom HUD statusline.
	statusLineDisabled bool
}

// getFS returns the filesystem to use, defaulting to the OS filesystem.
func (w *ClaudeCodeHookWriter) getFS() afero.Fs {
	return getFS(w.FS)
}

// HooksPath returns the path to Claude Code's settings.json file.
func (w *ClaudeCodeHookWriter) HooksPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "settings.json")
}

// MCPConfigPath returns the path to Claude Code's .mcp.json file.
// Note: MCP servers must be in .mcp.json (not settings.json) for ${CLAUDE_PROJECT_DIR}
// variable expansion to work. See: https://github.com/anthropics/claude-code/issues/4276
func (w *ClaudeCodeHookWriter) MCPConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".mcp.json")
}

// claudeCodeStatusLine represents the statusLine configuration in settings.json.
type claudeCodeStatusLine struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Padding int    `json:"padding,omitempty"`
}

// claudeCodeSettings represents the structure of .claude/settings.json
// Note: MCP servers are now stored in .mcp.json, not here.
type claudeCodeSettings struct {
	Hooks      map[string][]claudeCodeHookMatcher `json:"hooks,omitempty"`
	StatusLine *claudeCodeStatusLine              `json:"statusLine,omitempty"`
	// Preserve other settings (including legacy mcpServers for backwards compat)
	Other map[string]json.RawMessage `json:"-"`
}

// claudeCodeMCPConfig represents the structure of .mcp.json
// This file supports ${CLAUDE_PROJECT_DIR} variable expansion.
type claudeCodeMCPConfig struct {
	MCPServers map[string]claudeCodeMCPServer `json:"mcpServers,omitempty"`
}

// claudeCodeMCPServer represents an MCP server configuration in Claude Code format.
type claudeCodeMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`      // Working directory for the server
	SCM     string   `json:"_ctxloom,omitempty"` // Marker identifying ctxloom-managed servers
}

// claudeCodeHookMatcher represents a hook matcher entry in Claude Code format.
type claudeCodeHookMatcher struct {
	Matcher string           `json:"matcher,omitempty"`
	Hooks   []claudeCodeHook `json:"hooks"`
}

// claudeCodeHook represents a single hook in Claude Code format.
//
// Note: The SCM field is intentionally NOT serialized to JSON (json:"-").
// Claude Code uses Zod schema validation with .strict() mode when validating
// edits to settings.json, which rejects unknown fields. Instead of relying on
// a marker field, we identify ctxloom-managed hooks by their executable token
// (the command's first word resolves to `ctxloom`) via isCtxloomManaged().
// This is path-agnostic: any `ctxloom <subcommand>` hook is recognized, so the
// callback subcommand can move without breaking detection or cleanup.
// See: claude-code-src/src/utils/settings/validation.ts:193
type claudeCodeHook struct {
	Type    string `json:"type,omitempty"`
	Command string `json:"command,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
	Async   bool   `json:"async,omitempty"`
	SCM     string `json:"-"` // Internal only - not serialized (Claude Code strict schema validation)
}

// WriteSettings implements SettingsWriter for Claude Code.
// Hooks are written to .claude/settings.json
// MCP servers are written to .mcp.json (where variable expansion works)
func (w *ClaudeCodeHookWriter) WriteSettings(hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, projectDir string) error {
	if hooks == nil {
		hooks = &wire.HooksConfig{}
	}

	fs := w.getFS()
	settingsPath := w.SettingsPath(projectDir)

	// Ensure .claude directory exists
	claudeDir := filepath.Dir(settingsPath)
	if err := fs.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("failed to create .claude directory: %w", err)
	}

	// Load existing settings
	settings, err := w.loadSettings(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to load existing settings: %w", err)
	}

	// Remove old ctxloom-managed hooks from settings
	w.removeCtxloomHooks(settings)

	// Add ctxloom hooks from unified config
	w.addUnifiedHooks(settings, hooks.Unified)

	// Add ctxloom hooks from backend-specific passthrough
	if backendHooks, ok := hooks.Plugins["claude-code"]; ok {
		w.addBackendHooks(settings, backendHooks)
	}

	// Configure statusLine if not already set by the user
	w.ensureStatusLine(settings)

	// Write hooks to settings.json
	if err := w.saveSettings(settingsPath, settings); err != nil {
		return err
	}

	// Write MCP servers to .mcp.json (separate file where variable expansion works)
	return w.writeMCPConfig(projectDir, mcp, bundleMCP)
}

// WriteHooks implements HookWriter for Claude Code (backwards compatible).
func (w *ClaudeCodeHookWriter) WriteHooks(cfg *wire.HooksConfig, projectDir string) error {
	return w.WriteSettings(cfg, nil, nil, projectDir)
}

// SettingsPath returns the path to Claude Code's settings.json file.
func (w *ClaudeCodeHookWriter) SettingsPath(projectDir string) string {
	return w.HooksPath(projectDir)
}

// loadSettings loads existing settings.json or returns empty settings.
// This function is fault-tolerant: on parse errors, it logs a warning and
// returns empty settings rather than failing, allowing ctxloom to continue.
func (w *ClaudeCodeHookWriter) loadSettings(path string) (*claudeCodeSettings, error) {
	settings := &claudeCodeSettings{
		Hooks: make(map[string][]claudeCodeHookMatcher),
		Other: make(map[string]json.RawMessage),
	}

	fs := w.getFS()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}

	// First unmarshal to get all fields
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		// Claude Code's settings.json format is undocumented and may change.
		// If we can't parse it, warn but continue with empty settings.
		// This ensures ctxloom doesn't block startup due to schema changes.
		w.warn("failed to parse settings.json (schema may have changed): %v - ctxloom hooks will be added but existing settings may not be preserved", err)
		return settings, nil
	}

	// Extract hooks separately
	if hooksRaw, ok := raw["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &settings.Hooks); err != nil {
			// Hooks format may have changed - warn but continue
			w.warn("failed to parse hooks in settings.json: %v - existing hooks may not be preserved", err)
			// Don't fail - just skip preserving existing hooks
		}
		delete(raw, "hooks")
	}

	// Extract statusLine separately
	if slRaw, ok := raw["statusLine"]; ok {
		var sl claudeCodeStatusLine
		if err := json.Unmarshal(slRaw, &sl); err != nil {
			w.warn("failed to parse statusLine in settings.json: %v", err)
		} else {
			settings.StatusLine = &sl
		}
		delete(raw, "statusLine")
	}

	// Remove mcpServers from settings.json if present (migrating to .mcp.json)
	delete(raw, "mcpServers")

	// Preserve other fields
	settings.Other = raw

	return settings, nil
}

// warn outputs a warning message to stderr.
func (w *ClaudeCodeHookWriter) warn(format string, args ...interface{}) {
	warn(format, args...)
}

// saveSettings writes settings back to settings.json.
// Note: MCP servers are written separately to .mcp.json
//
// This function implements two safety measures for Claude schema resilience:
// 1. Backup: Creates a .bak file before modifying (preserves original on schema changes)
// 2. Atomic write: Writes to temp file first, then renames (prevents corruption)
func (w *ClaudeCodeHookWriter) saveSettings(path string, settings *claudeCodeSettings) error {
	// Build output map starting with preserved fields
	output := make(map[string]interface{})
	for k, v := range settings.Other {
		var val interface{}
		if err := json.Unmarshal(v, &val); err != nil {
			w.warn("failed to preserve setting %q: %v", k, err)
			continue // Skip corrupted field
		}
		output[k] = val
	}

	// Add hooks if non-empty
	if len(settings.Hooks) > 0 {
		output["hooks"] = settings.Hooks
	}

	// Add statusLine if configured
	if settings.StatusLine != nil {
		output["statusLine"] = settings.StatusLine
	}

	// Note: mcpServers are NOT written here - they go to .mcp.json

	data, err := agent.CanonicalJSON(output)
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	return atomicWriteFile(w.getFS(), path, data, "settings")
}

// loadMCPConfig loads existing .mcp.json or returns empty config.
// This function is fault-tolerant: on parse errors, it logs a warning and
// returns empty config rather than failing, allowing ctxloom to continue.
func (w *ClaudeCodeHookWriter) loadMCPConfig(path string) (*claudeCodeMCPConfig, error) {
	mcpConfig := &claudeCodeMCPConfig{
		MCPServers: make(map[string]claudeCodeMCPServer),
	}

	fs := w.getFS()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if os.IsNotExist(err) {
			return mcpConfig, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, mcpConfig); err != nil {
		// MCP config format may have changed - warn but continue
		w.warn("failed to parse .mcp.json: %v - existing MCP servers may not be preserved", err)
		return mcpConfig, nil
	}

	if mcpConfig.MCPServers == nil {
		mcpConfig.MCPServers = make(map[string]claudeCodeMCPServer)
	}

	return mcpConfig, nil
}

// saveMCPConfig writes MCP config to .mcp.json.
// Uses backup and atomic write for safety (see saveSettings).
func (w *ClaudeCodeHookWriter) saveMCPConfig(path string, mcpConfig *claudeCodeMCPConfig) error {
	data, err := agent.CanonicalJSON(mcpConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal .mcp.json: %w", err)
	}

	return atomicWriteFile(w.getFS(), path, data, ".mcp.json")
}

// writeMCPConfig writes MCP servers to .mcp.json.
// This file supports ${CLAUDE_PROJECT_DIR} variable expansion.
func (w *ClaudeCodeHookWriter) writeMCPConfig(projectDir string, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) error {
	mcpPath := w.MCPConfigPath(projectDir)

	// Load existing MCP config
	mcpConfig, err := w.loadMCPConfig(mcpPath)
	if err != nil {
		return fmt.Errorf("failed to load existing .mcp.json: %w", err)
	}

	// Remove old ctxloom-managed MCP servers. We match the same way as
	// hooks: the SCM marker covers bundle/unified servers (arbitrary
	// commands like `npx …`), and isCtxloomManaged covers ctxloom's own
	// auto-registered server even if its marker ever drifts.
	for name, server := range mcpConfig.MCPServers {
		if server.SCM != "" || isCtxloomManaged(server.Command) {
			delete(mcpConfig.MCPServers, name)
		}
	}

	// Add MCP servers
	w.addMCPServersToConfig(mcpConfig, mcp, bundleMCP)

	// Write MCP config back
	return w.saveMCPConfig(mcpPath, mcpConfig)
}

// ensureStatusLine configures the ctxloom HUD statusline if not already set by the user.
// If the user has configured their own statusLine (not ctxloom-managed), it is preserved.
//
// The statusLine is a single dedicated slot, so we recognize ours by the
// executable alone (isCtxloomManaged) — not the verb. Keying on the exact
// `meta hud` path orphaned installs whenever the verb moved (the legacy
// `ctxloom hook hud` form): this saw an unrecognized command, assumed it
// was user-authored, and preserved it. The dead `hook hud` then dumped the
// `hook` help into the status bar on every render. Matching any
// ctxloom-emitted command lets apply migrate it forward.
func (w *ClaudeCodeHookWriter) ensureStatusLine(settings *claudeCodeSettings) {
	// If statusLine is set and NOT ctxloom-managed, respect the user's config
	if settings.StatusLine != nil && !isCtxloomManaged(settings.StatusLine.Command) {
		return
	}

	// Opt-out: don't manage a statusline. Clear any previously ctxloom-managed
	// one so the user ends up with their own choice (or none).
	if w.statusLineDisabled {
		settings.StatusLine = nil
		return
	}

	// Set or update ctxloom-managed statusLine. Bare `ctxloom` resolves
	// via PATH at fire time — see GetExecutablePath for the rationale
	// behind not baking an absolute path into the file.
	settings.StatusLine = &claudeCodeStatusLine{
		Type:    "command",
		Command: ctxloomBinary + " hook hud",
	}
}

// removeCtxloomHooks removes all ctxloom-managed hooks from settings.
// It identifies ctxloom hooks by command pattern: containing "ctxloom" AND "inject-context".
// The SCM field is checked for in-memory hooks but is not serialized to JSON
// (Claude Code uses strict schema validation that rejects unknown fields).
func (w *ClaudeCodeHookWriter) removeCtxloomHooks(settings *claudeCodeSettings) {
	for eventName, matchers := range settings.Hooks {
		var filteredMatchers []claudeCodeHookMatcher
		for _, matcher := range matchers {
			var filteredHooks []claudeCodeHook
			for _, hook := range matcher.Hooks {
				// Keep hooks that are NOT ctxloom-managed
				if hook.SCM == "" && !isCtxloomManaged(hook.Command) {
					filteredHooks = append(filteredHooks, hook)
				}
			}
			if len(filteredHooks) > 0 {
				matcher.Hooks = filteredHooks
				filteredMatchers = append(filteredMatchers, matcher)
			}
		}
		if len(filteredMatchers) > 0 {
			settings.Hooks[eventName] = filteredMatchers
		} else {
			delete(settings.Hooks, eventName)
		}
	}
}

// addUnifiedHooks translates unified hooks to Claude Code format and adds them.
func (w *ClaudeCodeHookWriter) addUnifiedHooks(settings *claudeCodeSettings, unified wire.UnifiedHooks) {
	// PreTool -> PreToolUse
	for _, h := range unified.PreTool {
		w.addHook(settings, "PreToolUse", h)
	}

	// PostTool -> PostToolUse
	for _, h := range unified.PostTool {
		w.addHook(settings, "PostToolUse", h)
	}

	// SessionStart -> SessionStart
	for _, h := range unified.SessionStart {
		w.addHook(settings, "SessionStart", h)
	}

	// SessionEnd -> SessionEnd
	for _, h := range unified.SessionEnd {
		w.addHook(settings, "SessionEnd", h)
	}

	// PreShell -> PreToolUse with Bash matcher
	for _, h := range unified.PreShell {
		hook := h
		if hook.Matcher == "" {
			hook.Matcher = "Bash"
		}
		w.addHook(settings, "PreToolUse", hook)
	}

	// PostFileEdit -> PostToolUse with Edit|Write matcher
	for _, h := range unified.PostFileEdit {
		hook := h
		if hook.Matcher == "" {
			hook.Matcher = "Edit|Write"
		}
		w.addHook(settings, "PostToolUse", hook)
	}
}

// addBackendHooks adds backend-specific passthrough hooks.
func (w *ClaudeCodeHookWriter) addBackendHooks(settings *claudeCodeSettings, backendHooks wire.BackendHooks) {
	for eventName, hooks := range backendHooks {
		for _, h := range hooks {
			w.addHook(settings, eventName, h)
		}
	}
}

// addHook adds a single hook to the settings for the given event.
func (w *ClaudeCodeHookWriter) addHook(settings *claudeCodeSettings, eventName string, h wire.Hook) {
	ccHook := claudeCodeHook{
		Type:    h.Type,
		Command: h.Command,
		Prompt:  h.Prompt,
		Timeout: h.Timeout,
		Async:   h.Async,
		SCM:     computeHookHash(h),
	}

	// Default type to "command"
	if ccHook.Type == "" {
		ccHook.Type = "command"
	}

	// Find or create matcher entry
	matcher := h.Matcher
	matchers := settings.Hooks[eventName]

	// Look for existing matcher with same pattern
	found := false
	for i, m := range matchers {
		if m.Matcher == matcher {
			matchers[i].Hooks = append(matchers[i].Hooks, ccHook)
			found = true
			break
		}
	}

	if !found {
		matchers = append(matchers, claudeCodeHookMatcher{
			Matcher: matcher,
			Hooks:   []claudeCodeHook{ccHook},
		})
	}

	settings.Hooks[eventName] = matchers
}

// AppMCPServerName is the name used for the ctxloom MCP server in settings.
const AppMCPServerName = agent.MCPServerName

// addMCPServersToConfig adds MCP servers from config to .mcp.json config.
func (w *ClaudeCodeHookWriter) addMCPServersToConfig(mcpConfig *claudeCodeMCPConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer) {
	if mcpConfig.MCPServers == nil {
		mcpConfig.MCPServers = make(map[string]claudeCodeMCPServer)
	}

	// Auto-register ctxloom's own MCP server unless disabled
	if mcp == nil || mcp.ShouldAutoRegisterCtxloom() {
		mcpConfig.MCPServers[AppMCPServerName] = claudeCodeMCPServer{
			Command: ctxloomBinary,
			Args:    ctxloomMCPArgs,
			Cwd:     "${CLAUDE_PROJECT_DIR}", // Run in project directory so findAppDir works
			SCM:     "ctxloom-auto",          // Marker for auto-registered ctxloom server
		}
	}

	// Add MCP servers from profile bundles (loaded first, can be overridden)
	for name, server := range bundleMCP {
		mcpConfig.MCPServers[name] = claudeCodeMCPServer{
			Command: server.Command,
			Args:    server.Args,
			SCM:     server.SCM, // Already marked ctxloom with bundle source
		}
	}

	if mcp == nil {
		return
	}

	// Add unified MCP servers (overrides bundle servers if same name)
	for name, server := range mcp.Servers {
		mcpConfig.MCPServers[name] = claudeCodeMCPServer{
			Command: server.Command,
			Args:    server.Args,
			SCM:     computeMCPServerHash(server), // Marker for ctxloom-managed
		}
	}

	// Add backend-specific MCP servers (passthrough)
	if backendServers, ok := mcp.Plugins["claude-code"]; ok {
		for name, server := range backendServers {
			mcpConfig.MCPServers[name] = claudeCodeMCPServer{
				Command: server.Command,
				Args:    server.Args,
				SCM:     computeMCPServerHash(server),
			}
		}
	}
}

// RemoveSettings implements SettingsWriter for Claude Code: it clears
// ctxloom-managed hooks and statusline from settings.json and ctxloom-marked
// servers from .mcp.json, touching neither file when it does not already exist.
func (w *ClaudeCodeHookWriter) RemoveSettings(projectDir string) error {
	fs := w.getFS()

	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); exists {
		settings, err := w.loadSettings(settingsPath)
		if err != nil {
			return fmt.Errorf("failed to load existing settings: %w", err)
		}
		w.removeCtxloomHooks(settings)
		if settings.StatusLine != nil && isCtxloomManaged(settings.StatusLine.Command) {
			settings.StatusLine = nil
		}
		if err := w.saveSettings(settingsPath, settings); err != nil {
			return err
		}
	}

	mcpPath := w.MCPConfigPath(projectDir)
	if exists, _ := afero.Exists(fs, mcpPath); exists {
		mcpConfig, err := w.loadMCPConfig(mcpPath)
		if err != nil {
			return fmt.Errorf("failed to load existing .mcp.json: %w", err)
		}
		for name, server := range mcpConfig.MCPServers {
			if server.SCM != "" {
				delete(mcpConfig.MCPServers, name)
			}
		}
		if err := w.saveMCPConfig(mcpPath, mcpConfig); err != nil {
			return err
		}
	}
	return nil
}

// Status implements SettingsWriter for Claude Code.
func (w *ClaudeCodeHookWriter) Status(projectDir string) (SettingsStatus, error) {
	fs := w.getFS()
	var status SettingsStatus

	settingsPath := w.SettingsPath(projectDir)
	if exists, _ := afero.Exists(fs, settingsPath); exists {
		status.SettingsExists = true
		settings, err := w.loadSettings(settingsPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing settings: %w", err)
		}
		status.HooksPresent = claudeHasManagedHook(settings)
		status.StatusLine = settings.StatusLine != nil && isCtxloomManaged(settings.StatusLine.Command)
	}

	mcpPath := w.MCPConfigPath(projectDir)
	if exists, _ := afero.Exists(fs, mcpPath); exists {
		mcpConfig, err := w.loadMCPConfig(mcpPath)
		if err != nil {
			return status, fmt.Errorf("failed to load existing .mcp.json: %w", err)
		}
		for _, server := range mcpConfig.MCPServers {
			if server.SCM != "" {
				status.MCPPresent = true
				break
			}
		}
	}
	return status, nil
}

// claudeHasManagedHook reports whether any configured hook is ctxloom-managed.
func claudeHasManagedHook(settings *claudeCodeSettings) bool {
	for _, matchers := range settings.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				if hook.SCM != "" || isCtxloomManaged(hook.Command) {
					return true
				}
			}
		}
	}
	return false
}
