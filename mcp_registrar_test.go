package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/shared/wire"
)

func TestMCPRegistrar_Name(t *testing.T) {
	assert.Equal(t, "claude-code", MCPRegistrar{}.Name())
}

func TestMCPRegistrar_ConfigPath(t *testing.T) {
	p, err := (MCPRegistrar{}).ConfigPath("/proj", false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/proj", ".mcp.json"), p)

	g, err := (MCPRegistrar{}).ConfigPath("/proj", true)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(g, ".claude.json"), g)
	assert.NotContains(t, g, "/proj", "global path is home-rooted")
}

func TestMCPRegistrar_InstallPreservesProvenanceKeys(t *testing.T) {
	existing := `{
	  "mcpServers": {
	    "ctxloom": {"_ctxloom": "ctxloom-auto", "command": "ctxloom", "cwd": "${CLAUDE_PROJECT_DIR}"}
	  }
	}`
	r := MCPRegistrar{}
	out, err := r.Install([]byte(existing), "taskloom", wire.MCPServer{Command: "taskloom", Args: []string{"mcp"}})
	require.NoError(t, err)

	var doc map[string]map[string]map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Equal(t, "ctxloom-auto", doc["mcpServers"]["ctxloom"]["_ctxloom"], "foreign provenance keys survive")
	assert.Equal(t, "${CLAUDE_PROJECT_DIR}", doc["mcpServers"]["ctxloom"]["cwd"])
	assert.Equal(t, "taskloom", doc["mcpServers"]["taskloom"]["command"])

	removed, err := r.Uninstall(out, "taskloom")
	require.NoError(t, err)
	ok, err := r.Installed(removed, "taskloom")
	require.NoError(t, err)
	assert.False(t, ok)
	foreign, err := r.Installed(removed, "ctxloom")
	require.NoError(t, err)
	assert.True(t, foreign)
}
