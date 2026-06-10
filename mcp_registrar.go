package claude

import (
	"os"
	"path/filepath"

	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
)

// MCPRegistrar implements agent.MCPRegistrar for Claude Code: project-scope
// servers live in `.mcp.json` at the project root (where variable expansion
// works), user-scope servers in `~/.claude.json` — both in the JSON
// "mcpServers" table shape.
type MCPRegistrar struct{}

var _ agent.MCPRegistrar = MCPRegistrar{}

// Name returns the agent identifier.
func (MCPRegistrar) Name() string { return "claude-code" }

// Present reports whether Claude Code appears to be in use for the scope.
func (MCPRegistrar) Present(dir string, global bool) bool {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		return pathExists(filepath.Join(home, ".claude.json")) || pathExists(filepath.Join(home, ".claude"))
	}
	return pathExists(filepath.Join(dir, ".mcp.json")) || pathExists(filepath.Join(dir, ".claude"))
}

// ConfigPath returns the MCP config file for the scope.
func (MCPRegistrar) ConfigPath(dir string, global bool) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude.json"), nil
	}
	return filepath.Join(dir, ".mcp.json"), nil
}

// Install merges the named server into the config bytes. Idempotent; foreign
// keys (including provenance fields like _ctxloom and cwd) are preserved.
func (MCPRegistrar) Install(config []byte, name string, server wire.MCPServer) ([]byte, error) {
	return agent.InstallMCPServerJSON(config, name, server)
}

// Uninstall removes the named server from the config bytes.
func (MCPRegistrar) Uninstall(config []byte, name string) ([]byte, error) {
	return agent.UninstallMCPServerJSON(config, name)
}

// Installed reports whether the named server is present in the config.
func (MCPRegistrar) Installed(config []byte, name string) (bool, error) {
	return agent.MCPServerInstalledJSON(config, name)
}

// pathExists reports whether the path exists (file or directory).
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
