package localclient

import (
	"testing"

	"github.com/0xmarkhydra/codelocal/internal/mcphub"
	plugindomain "github.com/0xmarkhydra/codelocal/internal/plugins"
)

func TestPluginMCPConfigUsesGlobalHTTPAndCredentialReference(t *testing.T) {
	config, err := pluginMCPConfig(map[string]any{
		"pluginId":  "github",
		"endpoint":  "https://mcp.example.com/mcp",
		"bearerEnv": "GITHUB_TOKEN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Name != "plugin-github" || config.Scope != "global" || config.Transport != "http" {
		t.Fatalf("unexpected config: %#v", config)
	}
	if config.URL != "https://mcp.example.com/mcp" {
		t.Fatalf("endpoint=%q", config.URL)
	}
	auth := config.Headers["Authorization"]
	if auth.Source != "GITHUB_TOKEN" || auth.Prefix != "Bearer " {
		t.Fatalf("authorization ref=%#v", auth)
	}
}

func TestPluginMCPConfigRejectsSecretsInURLAndInvalidEnv(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"url credentials": {"pluginId": "github", "endpoint": "https://token@example.com/mcp"},
		"public http":     {"pluginId": "github", "endpoint": "http://example.com/mcp"},
		"invalid env":     {"pluginId": "github", "endpoint": "https://example.com/mcp", "bearerEnv": "TOKEN=value"},
		"bad plugin id":   {"pluginId": "GitHub Plugin", "endpoint": "https://example.com/mcp"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pluginMCPConfig(args); err == nil {
				t.Fatalf("expected rejection for %#v", args)
			}
		})
	}
}

func TestPluginMCPConfigAllowsLoopbackHTTP(t *testing.T) {
	config, err := pluginMCPConfig(map[string]any{"pluginId": "dev-mcp", "endpoint": "http://127.0.0.1:3001/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if config.URL != "http://127.0.0.1:3001/mcp" {
		t.Fatalf("endpoint=%q", config.URL)
	}
}

func TestManagedPenpotAcceptsOnlyHostedEndpoint(t *testing.T) {
	endpoint, err := validateManagedPenpotEndpoint(plugindomain.ManagedPenpotMCPURL)
	if err != nil || endpoint != plugindomain.ManagedPenpotMCPURL {
		t.Fatalf("hosted endpoint rejected: endpoint=%q err=%v", endpoint, err)
	}
	if _, err := validateManagedPenpotEndpoint("https://example.com/mcp"); err == nil {
		t.Fatal("arbitrary endpoint accepted for managed Penpot")
	}
}

func TestPluginMCPServerNameIsStableAndBounded(t *testing.T) {
	longID := "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghij"
	first, err := pluginMCPServerName(longID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pluginMCPServerName(longID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) > 64 {
		t.Fatalf("server name=%q second=%q", first, second)
	}
}

func TestRemovePluginMCPClearsOnlyMatchingManagedSecret(t *testing.T) {
	engine, err := New(t.TempDir(), "workspace", "Workspace", "key", "device")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if _, err := engine.MCP.Add(mcphub.ServerConfig{Name: "plugin-github", Enabled: true, Scope: "global", Transport: "http", URL: "https://example.com/mcp"}); err != nil {
		t.Fatal(err)
	}
	ref := plugindomain.ManagedCredentialReference("github")
	engine.setRuntimeSecret(ref, "secret-value")
	if _, err := engine.removePluginMCP(map[string]any{"pluginId": "github", "credentialRef": ref}); err != nil {
		t.Fatal(err)
	}
	if _, exists := engine.runtimeSecrets[ref]; exists || len(engine.runtimeRedact) != 0 {
		t.Fatalf("managed secret remained materialized: secrets=%#v redact=%d", engine.runtimeSecrets, len(engine.runtimeRedact))
	}

	wrongRef := plugindomain.ManagedCredentialReference("notion")
	if _, err := engine.removePluginMCP(map[string]any{"pluginId": "github", "credentialRef": wrongRef}); err == nil {
		t.Fatal("cross-Plugin credential cleanup was accepted")
	}
}

func TestRemoveManagedPenpotClearsKeyButKeepsSystemServer(t *testing.T) {
	engine, err := New(t.TempDir(), "workspace", "Workspace", "key", "device")
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	ref := managedPenpotCredentialRef()
	engine.SetRuntimeEnvironment(nil, map[string]string{ref: "secret-value"})
	result, err := engine.removePluginMCP(map[string]any{"pluginId": managedPenpotPluginID, "credentialRef": ref})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["serverName"] != managedPenpotPluginID {
		t.Fatalf("unexpected disconnect result: %#v", result)
	}
	if _, exists := engine.runtimeSecrets[ref]; exists {
		t.Fatal("managed Penpot key remained materialized")
	}
	listed, err := engine.MCP.List()
	if err != nil {
		t.Fatal(err)
	}
	servers := listed.([]map[string]any)
	// Global MCP settings may already contain unrelated servers on this machine.
	// Disconnecting Penpot must preserve its managed system entry regardless.
	foundPenpot := false
	for _, server := range servers {
		if server["name"] == managedPenpotPluginID {
			foundPenpot = true
			if server["managed"] != true {
				t.Fatalf("Penpot is no longer a managed system server: %#v", server)
			}
		}
	}
	if !foundPenpot {
		t.Fatalf("system Penpot server was removed: %#v", servers)
	}
}
