package claude

import "encoding/json"

// This file is the single source of truth for Claude Code's PreToolUse hook
// wire protocol: the JSON Claude Code writes to a hook's stdin and the
// decision JSON it accepts on stdout. Consumers that sit on the hook wire
// (ltk's claude-code engine) import these types instead of redefining them.
//
// Contract (verified May 2026):
//   - Allow / pass-through: emit nothing, exit 0. The normal permission flow
//     proceeds (this does NOT auto-approve).
//   - Deny: stdout {"hookSpecificOutput":{"hookEventName":"PreToolUse",
//     "permissionDecision":"deny","permissionDecisionReason":"…"}}, exit 0.
//     The reason is fed back to the model.

// HookEventPreToolUse is the event name carried in payloads and decisions.
const HookEventPreToolUse = "PreToolUse"

// PermissionDeny is the permissionDecision value that blocks the tool call.
const PermissionDeny = "deny"

// HookPayload is the JSON Claude Code writes to a PreToolUse hook's stdin.
type HookPayload struct {
	SessionID      string    `json:"session_id,omitempty"`
	TranscriptPath string    `json:"transcript_path,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	PermissionMode string    `json:"permission_mode,omitempty"`
	HookEventName  string    `json:"hook_event_name,omitempty"`
	ToolName       string    `json:"tool_name"`
	ToolInput      ToolInput `json:"tool_input"`
}

// ToolInput carries the union of tool-input fields ltk-style consumers need:
// the command for Bash/PowerShell and the target for the file-editing tools —
// file_path for Edit/Write/MultiEdit, notebook_path for NotebookEdit (which
// does not send file_path).
type ToolInput struct {
	Command      string `json:"command,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
	NotebookPath string `json:"notebook_path,omitempty"`
}

// HookOutput is the decision JSON a hook writes to stdout to deny a tool
// call. Allowing requires no output at all.
type HookOutput struct {
	HookSpecificOutput HookSpecificOutput `json:"hookSpecificOutput"`
}

// HookSpecificOutput is the PreToolUse permission decision.
type HookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// --- SessionStart wire shapes ----------------------------------------------
//
// SessionStart is the context-injection event: Claude Code (and Codex, which
// adopted the same shape) writes session identity to the hook's stdin and
// accepts an additionalContext envelope on stdout. ctxloom's hook targets
// (`ctxloom hook inject-context`, `ctxloom hook session-bind`) sit on this
// wire; they import these types instead of redefining them.

// HookEventSessionStart is the event name carried in SessionStart decisions.
const HookEventSessionStart = "SessionStart"

// SessionStartPayload is the JSON written to a SessionStart hook's stdin.
// Claude Code provides session_id (and computes transcript_path); Codex
// provides transcript_path directly. source distinguishes the launch kind.
type SessionStartPayload struct {
	SessionID      string `json:"session_id"`      // Claude Code: session identifier
	TranscriptPath string `json:"transcript_path"` // Codex: full path to transcript file
	Source         string `json:"source"`          // Claude Code SessionStart source: startup|resume|clear|compact
}

// SessionStartOutput is the JSON a SessionStart hook writes to stdout to
// inject context. An empty output (no hookSpecificOutput) injects nothing.
type SessionStartOutput struct {
	HookSpecificOutput *SessionStartSpecificOutput `json:"hookSpecificOutput,omitempty"`
	// SystemMessage rides a separate channel from HookSpecificOutput: Claude
	// Code surfaces it to the user in the terminal, NOT to the model. ctxloom
	// uses it to nudge the user toward /recover after a /clear, where the model
	// gets no recovered context but the human should know it can be pulled back.
	SystemMessage string `json:"systemMessage,omitempty"`
}

// SessionStartSpecificOutput carries the additional context to inject.
type SessionStartSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

// DecodeHookPayload parses a hook stdin payload.
func DecodeHookPayload(data []byte) (HookPayload, error) {
	var p HookPayload
	err := json.Unmarshal(data, &p)
	return p, err
}

// EncodeDeny renders the deny decision Claude Code expects on stdout.
func EncodeDeny(reason string) ([]byte, error) {
	return json.Marshal(HookOutput{HookSpecificOutput: HookSpecificOutput{
		HookEventName:            HookEventPreToolUse,
		PermissionDecision:       PermissionDeny,
		PermissionDecisionReason: reason,
	}})
}
