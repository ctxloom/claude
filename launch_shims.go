package claude

import "github.com/ctxloom/shared/agent"

// Shims to the shared launch core (internal/agent) so the launch backend and
// capabilities below read unchanged after the move from internal/lm/backends.
// Transitional — inline to agent.* later. The settings-writer shims (warn,
// getFS, computeHookHash, ...) live alongside the writer in claude.go.
type (
	BaseBackend         = agent.BaseBackend
	BaseLifecycle       = agent.BaseLifecycle
	BaseMCPManager      = agent.BaseMCPManager
	BaseContextProvider = agent.BaseContextProvider

	LifecycleHandler = agent.LifecycleHandler
	SkillRegistry    = agent.SkillRegistry
	ContextProvider  = agent.ContextProvider
	MCPManager       = agent.MCPManager
	MCPServer        = agent.MCPServer
	SessionHistory   = agent.SessionHistory
	EventHandler     = agent.EventHandler
	ToolEvent        = agent.ToolEvent

	Skill            = agent.Skill
	Session          = agent.Session
	SessionMeta      = agent.SessionMeta
	SessionEntry     = agent.SessionEntry
	SessionEntryType = agent.SessionEntryType
	Fragment         = agent.Fragment

	SetupRequest   = agent.SetupRequest
	ExecuteRequest = agent.ExecuteRequest
	ExecuteResult  = agent.ExecuteResult
	ModelInfo      = agent.ModelInfo
	BackendConfig  = agent.BackendConfig
)

const (
	ModeInteractive = agent.ModeInteractive
	ModeOneshot     = agent.ModeOneshot

	EntryTypeUser       = agent.EntryTypeUser
	EntryTypeAssistant  = agent.EntryTypeAssistant
	EntryTypeSystem     = agent.EntryTypeSystem
	EntryTypeToolUse    = agent.EntryTypeToolUse
	EntryTypeToolResult = agent.EntryTypeToolResult

	BeforeToolUse = agent.BeforeToolUse
	AfterToolUse  = agent.AfterToolUse

	SCMContextFileEnv = agent.SCMContextFileEnv
	SCMContextSubdir  = agent.SCMContextSubdir
)

var (
	NewBaseBackend         = agent.NewBaseBackend
	NewBaseLifecycle       = agent.NewBaseLifecycle
	NewBaseMCPManager      = agent.NewBaseMCPManager
	NewBaseContextProvider = agent.NewBaseContextProvider

	GetPromptContent = agent.GetPromptContent
)
