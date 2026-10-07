package cloudserver

import (
	"strings"
	"testing"

	"github.com/0xmarkhydra/codelocal/internal/mcpconfig"
)

func TestParseScopedMCPUpdateRejectsMultipleServers(t *testing.T) {
	raw := `{
		"mcpServers": {
			"blender": {"command": "uvx", "args": ["blender-mcp"]},
			"penpot": {"url": "https://design.example.com/mcp"}
		}
	}`

	_, err := parseScopedMCPUpdate(raw, mcpconfig.ModeLocal, "blender")
	if err == nil {
		t.Fatal("expected scoped edit to reject multiple MCP servers")
	}
	if !strings.Contains(err.Error(), "exactly one server") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseScopedMCPUpdateRejectsRenamedServer(t *testing.T) {
	raw := `{
		"mcpServers": {
			"penpot": {"command": "uvx", "args": ["penpot-mcp"]}
		}
	}`

	_, err := parseScopedMCPUpdate(raw, mcpconfig.ModeLocal, "blender")
	if err == nil {
		t.Fatal("expected scoped edit to reject a different MCP name")
	}
	if !strings.Contains(err.Error(), "must match") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseScopedMCPUpdateAcceptsOnlyTargetServer(t *testing.T) {
	raw := `{
		"mcpServers": {
			"blender": {"command": "uvx", "args": ["blender-mcp"]}
		}
	}`

	server, err := parseScopedMCPUpdate(raw, mcpconfig.ModeLocal, "blender")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server.Name != "blender" {
		t.Fatalf("expected blender, got %q", server.Name)
	}
	if server.Command != "uvx" {
		t.Fatalf("expected command uvx, got %q", server.Command)
	}
}
