package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/shared/agent"
)

// ClaudeConfig is claude-code's typed LLM config: the fields a claude-code
// labeled entry may carry. The backend owns this struct; the config package
// only carries the raw body that decodes into it.
type ClaudeConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (ClaudeConfig) BackendType() string { return "claude-code" }

// ClaudeCode implements the Backend interface for Claude Code CLI. The shared
// launch core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; ClaudeCode adds only the Claude-specific Configure/Execute.
type ClaudeCode struct {
	agent.LaunchBackend
	writeSettings agent.WriteSettingsFunc
	// openChatTransport opens the stream-json transport for the StructuredChat
	// capability. Defaults to spawnChatTransport (a real `claude` subprocess);
	// tests override it with in-memory pipes so they never spawn a process.
	openChatTransport chatTransportFunc
	// now stamps chat-stream entries that arrive without a timestamp — claude-code's
	// stream-json carries no per-event time, so we record receipt time here so the
	// protocol always has one. Injected for deterministic tests; nil means time.Now.
	now func() time.Time
}

// NewClaudeCode creates a new Claude Code backend with default settings. The
// writeSettings dispatch is injected (the registry supplies it) so the launch
// bases can write settings without importing the registry.
func NewClaudeCode(writeSettings agent.WriteSettingsFunc) *ClaudeCode {
	b := &ClaudeCode{writeSettings: writeSettings}
	b.BaseBackend = agent.NewBaseBackend("claude-code", "1.0.0")
	b.BinaryPath = "claude"
	b.InitLaunch(
		agent.NewBaseLifecycle("claude-code", b.writeSettings),
		&ClaudeSkills{},
		agent.NewBaseContextProvider(),
		NewClaudeSessionHistory(b),
	)
	return b
}

// Configure applies a decoded claude-code config to this backend.
func (b *ClaudeCode) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*ClaudeConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
	}
}

// Execute runs the backend with the given request.
func (b *ClaudeCode) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// Best-effort model identity. In minimal mode this is overwritten below with
	// the real id the CLI reports; otherwise it is the requested model, falling
	// back to the backend name rather than a fabricated version.
	modelName := req.Model
	if modelName == "" {
		modelName = b.Name()
	}
	modelInfo := &agent.ModelInfo{
		ModelName: modelName,
		Provider:  "anthropic",
	}

	// Dry run
	if req.DryRun {
		return &agent.ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}

	// Build args
	args := b.buildArgs(req)

	// Verbosity level 16+: show command
	if req.Verbosity >= 16 {
		_, _ = fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	// Build env with context file path
	env := make(map[string]string)
	for k, v := range req.Env {
		env[k] = v
	}
	if b.ContextFilePath() != "" {
		env[agent.SCMContextFileEnv] = b.ContextFilePath()
	}

	// Minimal oneshot runs with --output-format json: buffer the envelope,
	// emit the assistant text, and record the model the CLI actually used.
	if req.Mode == agent.ModeOneshot && req.SkipSetup {
		var raw bytes.Buffer
		exitCode, err := b.RunNonInteractive(ctx, args, env, &raw, stderr)
		text, model, perr := parseClaudeJSONResult(raw.Bytes())
		if perr != nil {
			// Fault tolerant: hand back whatever the CLI emitted and keep the
			// best-effort model rather than dropping the result.
			_, _ = stdout.Write(raw.Bytes())
			return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
		}
		_, _ = io.WriteString(stdout, text)
		if model != "" {
			modelInfo.ModelName = model
		}
		return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
	}

	// Run based on mode
	var exitCode int32
	var err error
	if req.Mode == agent.ModeInteractive {
		exitCode, err = b.RunInteractive(ctx, args, env, req.Stdin, stdout, stderr, req.Resize)
	} else {
		exitCode, err = b.RunNonInteractive(ctx, args, env, stdout, stderr)
	}

	return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// claudeJSONResult is the subset of the `claude --output-format json` envelope
// we consume: the assistant text and per-model token usage.
type claudeJSONResult struct {
	Result     string                      `json:"result"`
	ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
}

// claudeModelUsage is the per-model usage block. OutputTokens identifies the
// model that generated the result: the CLI may route a large read through an
// ancillary fast model (high input, tiny output) while the requested model does
// the actual generation, so output — not input — marks the working model.
type claudeModelUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// parseClaudeJSONResult extracts the result text and the resolved model id from
// a Claude CLI JSON envelope. The model is the modelUsage key with the most
// output tokens — the one that produced the result — so provenance records the
// generating model rather than a helper the CLI routed a read through. Ties
// break on sorted id for determinism.
func parseClaudeJSONResult(data []byte) (text, model string, err error) {
	var env claudeJSONResult
	if err := json.Unmarshal(data, &env); err != nil {
		return "", "", err
	}
	ids := make([]string, 0, len(env.ModelUsage))
	for id := range env.ModelUsage {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	best := ""
	bestTokens := -1
	for _, id := range ids {
		if env.ModelUsage[id].OutputTokens > bestTokens {
			best, bestTokens = id, env.ModelUsage[id].OutputTokens
		}
	}
	return env.Result, best, nil
}

// buildArgs constructs the command-line arguments.
func (b *ClaudeCode) buildArgs(req *agent.ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if req.AutoApprove {
		args = append(args, "--dangerously-skip-permissions")
	}

	// The model is resolved by the caller (the fast role's labeled config for
	// compression, the primary role's for coding); the backend no longer
	// substitutes a fast-model default. An empty model lets the CLI pick.
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	if req.Mode == agent.ModeOneshot {
		args = append(args, "--print")
	}

	// Minimal mode for distillation/compaction - skip all unnecessary startup.
	if req.SkipSetup {
		args = append(args,
			// JSON envelope carries the resolved model id (modelUsage), letting
			// Execute record the real model instead of guessing. The result is
			// machine-consumed here, so we lose nothing by buffering it.
			"--output-format", "json",
			"--tools", "", // Disable all tools
			"--disable-slash-commands", // No slash commands
			"--no-session-persistence", // Don't save session
			"--strict-mcp-config",      // ignore .mcp.json / external MCP servers
			"--system-prompt", "",      // drop CLAUDE.md/memory/identity so they don't pollute the result
			// Isolate via in-line overrides rather than `--setting-sources ""`:
			// an empty source list also drops the model config, so the CLI routes
			// generation to its built-in fast model regardless of --model. These
			// overrides disable hooks/MCP/attribution while leaving the requested
			// model in force.
			"--settings", minimalSettings(req.Model),
		)
	}

	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, prompt)
	}

	return args
}

// minimalSettings builds the JSON passed to `claude --settings` for headless
// distill/compaction. It overrides the loaded settings to an isolated baseline —
// no hooks, no project MCP, no attribution/cleanup, permissions bypassed — while
// keeping the requested model in force (an empty `--setting-sources` would drop
// the model config and route generation to the CLI's fast model). model is
// omitted when empty so the CLI default applies.
func minimalSettings(model string) string {
	s := map[string]any{
		"hooks":                      map[string]any{},
		"enableAllProjectMcpServers": false,
		"enabledMcpjsonServers":      []string{},
		"includeCoAuthoredBy":        false,
		"cleanupPeriodDays":          0,
		"permissions":                map[string]any{"defaultMode": "bypassPermissions"},
	}
	if model != "" {
		s["model"] = model
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}
