package mcpgateway

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPublicToolSurfaceGenerationFifteenKeepsCompactCatalog(t *testing.T) {
	first := PublicToolSurface()
	second := PublicToolSurface()
	if first != second {
		t.Fatalf("tool surface must be stable within a process: %#v != %#v", first, second)
	}
	if first.Version != PublicToolSurfaceVersion {
		t.Fatalf("surface version=%d want public version=%d", first.Version, PublicToolSurfaceVersion)
	}
	if first.Version != 15 {
		t.Fatalf("surface version=%d want generation 15", first.Version)
	}
	if first.Count != len(compactToolDefinitions()) || first.Count != 14 {
		t.Fatalf("generation 15 should keep exactly 14 tools: surface=%d registry=%d", first.Count, len(compactToolDefinitions()))
	}
	if len(first.Hash) != 64 {
		t.Fatalf("generation-15 surface hash must be sha256: %q", first.Hash)
	}
	for _, name := range []string{"workspace", "context", "terminal", "blog", "browser", "computer"} {
		if _, ok := currentPublicToolNames()[name]; !ok {
			t.Fatalf("generation 15 must advertise %s", name)
		}
	}
	for _, removed := range []string{"device", "project", "dependency", "lsp", "process", "approvals", "security", "mobile"} {
		if _, ok := currentPublicToolNames()[removed]; ok {
			t.Fatalf("generation 15 must not advertise grouped/internal tool %s", removed)
		}
	}
	publishArtifact := false
	for _, def := range compactToolDefinitions() {
		if def.Name != "terminal" {
			continue
		}
		for _, action := range allCompactActions(t, def) {
			if action == "publish_artifact" {
				publishArtifact = true
				break
			}
		}
	}
	if !publishArtifact {
		t.Fatal("generation 14 terminal schema must advertise publish_artifact")
	}
}

func TestCurrentMCPContractExplainsUnfinishedAgentWorkWithoutLegacyDrift(t *testing.T) {
	current := publicMCPInstructions()
	if !strings.Contains(current, "status=needs_continuation") || !strings.Contains(current, "not a finished user task") {
		t.Fatal("current MCP instructions must tell hosts how to continue incomplete tasks")
	}
	if strings.Contains(legacyPublicMCPInstructions(), "status=needs_continuation") {
		t.Fatal("pinned legacy instructions must remain byte-identical")
	}
	agent := compactDefinitionsByName()["agent"]
	if !strings.Contains(agent.Description, "status=needs_continuation") {
		t.Fatal("agent tool description must carry the continuation signal for hosts that omit server instructions")
	}
}

func TestGenerationFourPreservesGenerationTwoABI(t *testing.T) {
	legacy := legacyPublicToolDefinitions()
	if len(legacy) != 20 {
		t.Fatalf("legacy generation must retain 20 tools, got %d", len(legacy))
	}
	if got := legacyPublicToolSurfaceHash(); got != PinnedLegacyPublicToolSurfaceHash {
		t.Fatalf("generation-2 tool surface drifted: got %s want %s", got, PinnedLegacyPublicToolSurfaceHash)
	}
	if got := legacyPublicToolContractHash(); got != PinnedLegacyPublicToolContractHash {
		t.Fatalf("generation-2 tool contract drifted: got %s want %s", got, PinnedLegacyPublicToolContractHash)
	}
	if PinnedLegacyPublicMCPImplementationVersion != "1.5.16" {
		t.Fatalf("legacy MCP implementation identity drifted: %q", PinnedLegacyPublicMCPImplementationVersion)
	}
	expectedVersion := fmt.Sprintf("1.5.%d", publicMCPImplementationVersionBasePatch+PublicToolSurfaceVersion)
	if PublicMCPImplementationVersion != expectedVersion {
		t.Fatalf("public MCP identity must be derived from tool surface generation: got %q want %q", PublicMCPImplementationVersion, expectedVersion)
	}
	if PublicMCPImplementationVersion == PinnedLegacyPublicMCPImplementationVersion {
		t.Fatalf("new public tool generation must advance MCP implementation identity so AI hosts invalidate cached catalogs: %q", PublicMCPImplementationVersion)
	}
	current := publicToolContractHash()
	if len(current) != 64 || current == PinnedLegacyPublicToolContractHash {
		t.Fatalf("generation-3 contract hash must be a distinct sha256: %q", current)
	}
}

func TestBlogToolContractIsActionDrivenAndCloudScoped(t *testing.T) {
	defs := compactBlogToolDefinitions()
	if len(defs) != 1 || defs[0].Name != "blog" {
		t.Fatalf("unexpected blog definitions: %#v", defs)
	}
	def := defs[0]
	if def.Resolve != nil || def.Execute == nil {
		t.Fatal("blog must execute against cloud Store directly rather than route through local workspace operations")
	}
	if def.Annotations == nil || def.Annotations.ReadOnlyHint || !annotationFlag(def.Annotations.DestructiveHint) || annotationFlag(def.Annotations.OpenWorldHint) {
		t.Fatalf("unexpected blog annotations: %#v", def.Annotations)
	}
	fileParams, ok := def.Meta["openai/fileParams"].([]string)
	if !ok || !reflect.DeepEqual(fileParams, []string{"file"}) {
		t.Fatalf("blog file params=%#v", def.Meta["openai/fileParams"])
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.Required, []string{"action"}) {
		t.Fatalf("blog schema required=%v", schema.Required)
	}
	if !reflect.DeepEqual(schema.Properties["action"].Enum, blogToolActions) {
		t.Fatalf("blog actions=%v want=%v", schema.Properties["action"].Enum, blogToolActions)
	}
	if _, exists := schema.Properties["workspaceKey"]; exists {
		t.Fatal("cloud Blog tool must not require or advertise a local workspaceKey")
	}
}

func TestUnsupportedActionExplainsSchemaRecovery(t *testing.T) {
	err := unsupportedActionSchemaError("old_action")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	message := err.Error()
	for _, want := range []string{"old_action", "tool surface", "reconnect or refresh CodeLocal"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error missing %q: %s", want, message)
		}
	}
}

func TestToolResultsCarrySurfaceFingerprintWithoutTextInflation(t *testing.T) {
	result := textResult(map[string]any{"ok": true}, false)
	root, ok := result.StructuredContent.(map[string]any)
	if !ok || root == nil {
		t.Fatalf("structured content=%#v", result.StructuredContent)
	}
	surface, ok := root["codeLocalToolSurface"].(ToolSurfaceInfo)
	if !ok {
		t.Fatalf("missing typed tool surface metadata: %#v", root["codeLocalToolSurface"])
	}
	if surface != PublicToolSurface() {
		t.Fatalf("surface metadata=%#v want %#v", surface, PublicToolSurface())
	}
	if len(result.Content) == 0 || strings.Contains(result.Content[0].(*mcp.TextContent).Text, surface.Hash) {
		t.Fatal("tool surface fingerprint should stay in structured metadata unless a compatibility notice is needed")
	}
}

func TestPublicInstructionsExplainSessionlessWorkspaceRouting(t *testing.T) {
	instructions := publicMCPInstructions()
	for _, required := range []string{
		"workspace(action=list)",
		"workspaceKey explicitly",
		"never chooses between multiple projects using recency/LastSeenAt",
		"makeDefault=true",
		"never implicitly routes to managed system projects",
		"workspace(action=execution",
		"Never auto-switch execution mode",
	} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("public instructions must explain workspace routing; missing %q", required)
		}
	}
	// The pinned legacy instruction text must not drift: older clients verify a
	// published contract hash over this exact string.
	if strings.Contains(legacyPublicMCPInstructions(), "makeDefault=true") {
		t.Fatal("legacy instructions must stay byte-identical to the published generation")
	}
	if got := legacyPublicToolContractHash(); got != PinnedLegacyPublicToolContractHash {
		t.Fatalf("legacy contract hash drifted: %s", got)
	}
	if got := legacyPublicToolSurfaceHash(); got != PinnedLegacyPublicToolSurfaceHash {
		t.Fatalf("legacy surface hash drifted: %s", got)
	}
}
