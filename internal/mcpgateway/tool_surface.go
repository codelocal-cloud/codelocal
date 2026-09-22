package mcpgateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PublicToolSurfaceVersion is the compatibility generation of the public MCP
// contract. Generation 15 keeps the compact 14-tool catalog and refreshes
// cached MCP instructions so hosts see the explicit unfinished-task signal.
// Authenticated execution remains OAuth-protected. Bump the generation whenever
// the public contract or auth/discovery behavior changes.
const (
	PublicToolSurfaceVersion = 15

	// Version 1.5.16 was the generation-2 MCP identity. Keep the same release
	// line and derive the patch from the surface generation so every future
	// public catalog generation automatically changes the server identity seen
	// by AI hosts. This invalidates cached tools/list catalogs without requiring
	// developers to remember a second manual version bump.
	publicMCPImplementationVersionBasePatch = 14

	PinnedLegacyPublicToolSurfaceVersion       = 2
	PinnedLegacyPublicMCPImplementationVersion = "1.5.16"
	PinnedLegacyPublicToolSurfaceHash          = "780206fb4c6f4b53162bc3080060d1b14978900bf29bdbda50edfad366e0864c"
	PinnedLegacyPublicToolContractHash         = "2f236697108144b7bf9d2e5296c6e20fd021d4739f0bce298c663bd4cce49cca"

	// Backward source-compatibility aliases. Generation-3 tests intentionally
	// use the Legacy names so future readers do not mistake these for the hash
	// of the 21-tool generation.
	PinnedPublicToolSurfaceHash  = PinnedLegacyPublicToolSurfaceHash
	PinnedPublicToolContractHash = PinnedLegacyPublicToolContractHash
)

var PublicMCPImplementationVersion = fmt.Sprintf("1.5.%d", publicMCPImplementationVersionBasePatch+PublicToolSurfaceVersion)

type ToolSurfaceInfo struct {
	Version int    `json:"version"`
	Hash    string `json:"hash"`
	Count   int    `json:"count"`
}

type toolSurfaceFingerprint struct {
	Name        string          `json:"name"`
	Schema      json.RawMessage `json:"schema"`
	Meta        mcp.Meta        `json:"meta,omitempty"`
	ReadOnly    bool            `json:"readOnly"`
	Destructive bool            `json:"destructive"`
	OpenWorld   bool            `json:"openWorld"`
}

type toolContractFingerprint struct {
	Name            string          `json:"name"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Schema          json.RawMessage `json:"schema"`
	Meta            mcp.Meta        `json:"meta,omitempty"`
	AnnotationTitle string          `json:"annotationTitle"`
	ReadOnly        bool            `json:"readOnly"`
	Destructive     bool            `json:"destructive"`
	OpenWorld       bool            `json:"openWorld"`
}

func annotationFlag(value *bool) bool {
	return value != nil && *value
}

func canonicalSchema(raw json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return raw
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return canonical
}

func toolSurfaceFingerprintForDefinitions(defs []compactToolDef) []toolSurfaceFingerprint {
	fingerprints := make([]toolSurfaceFingerprint, 0, len(defs))
	for _, def := range defs {
		fingerprint := toolSurfaceFingerprint{Name: def.Name, Schema: canonicalSchema(def.Schema), Meta: def.Meta}
		if def.Annotations != nil {
			fingerprint.ReadOnly = def.Annotations.ReadOnlyHint
			fingerprint.Destructive = annotationFlag(def.Annotations.DestructiveHint)
			fingerprint.OpenWorld = annotationFlag(def.Annotations.OpenWorldHint)
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Slice(fingerprints, func(i, j int) bool { return fingerprints[i].Name < fingerprints[j].Name })
	return fingerprints
}

func toolSurfaceHashForDefinitions(defs []compactToolDef) string {
	raw, _ := json.Marshal(toolSurfaceFingerprintForDefinitions(defs))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func toolContractHashForDefinitionsWithVersion(defs []compactToolDef, instructions, implementationVersion string) string {
	fingerprints := make([]toolContractFingerprint, 0, len(defs))
	for _, def := range defs {
		fingerprint := toolContractFingerprint{
			Name:        def.Name,
			Title:       def.Title,
			Description: def.Description,
			Schema:      canonicalSchema(def.Schema),
			Meta:        def.Meta,
		}
		if def.Annotations != nil {
			fingerprint.AnnotationTitle = def.Annotations.Title
			fingerprint.ReadOnly = def.Annotations.ReadOnlyHint
			fingerprint.Destructive = annotationFlag(def.Annotations.DestructiveHint)
			fingerprint.OpenWorld = annotationFlag(def.Annotations.OpenWorldHint)
		}
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Slice(fingerprints, func(i, j int) bool { return fingerprints[i].Name < fingerprints[j].Name })
	payload := struct {
		ImplementationVersion string                    `json:"implementationVersion"`
		Instructions          string                    `json:"instructions"`
		Tools                 []toolContractFingerprint `json:"tools"`
	}{
		ImplementationVersion: implementationVersion,
		Instructions:          instructions,
		Tools:                 fingerprints,
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func toolContractHashForDefinitions(defs []compactToolDef, instructions string) string {
	return toolContractHashForDefinitionsWithVersion(defs, instructions, PublicMCPImplementationVersion)
}

func legacyPublicToolDefinitions() []compactToolDef {
	defs := generationFourCompactToolDefinitions()
	legacy := make([]compactToolDef, 0, len(defs))
	for _, def := range defs {
		if def.Name != "blog" {
			legacy = append(legacy, def)
		}
	}
	return legacy
}

func legacyToolSurfaceSummary() string {
	return fmt.Sprintf("CodeLocal tool surface v%d (%d tools, sha256:%s)", PinnedLegacyPublicToolSurfaceVersion, 20, PinnedLegacyPublicToolSurfaceHash)
}

func generationFourOrchestrationInstructions() string {
	const current = "For coding/debugging/review/refactor, call context(action=task) early; use context's project/dependency/LSP actions for exact relationships, read only for targeted expansion, and search mainly for literal/config/log text. Use edit for mutations, then verify and the smallest relevant terminal checks. Use terminal for both command execution and process lifecycle, git only for Git work, and mcp lazily for installed extensions."
	const previous = "For coding/debugging/review/refactor, call context early; use lsp for exact code relationships, read only for targeted expansion, and search mainly for literal/config/log text. Use edit for mutations, then verify and the smallest relevant terminal checks. Use terminal plus process for execution lifecycle, git only for Git work, and mcp lazily for installed extensions."
	return strings.Replace(compactOrchestrationInstructions, current, previous, 1)
}

func legacyPublicMCPInstructions() string {
	return generationFourOrchestrationInstructions() + "\n\nCompatibility: " + legacyToolSurfaceSummary() + ". Legacy tool calls that CodeLocal can translate remain supported without user action. Only CODELOCAL_TOOL_SCHEMA_MISMATCH means the client requested a contract CodeLocal cannot translate."
}

func legacyPublicToolSurfaceHash() string {
	return toolSurfaceHashForDefinitions(legacyPublicToolDefinitions())
}

func legacyPublicToolContractHash() string {
	return toolContractHashForDefinitionsWithVersion(legacyPublicToolDefinitions(), legacyPublicMCPInstructions(), PinnedLegacyPublicMCPImplementationVersion)
}

var (
	toolSurfaceOnce  sync.Once
	toolSurfaceCache ToolSurfaceInfo
	toolNamesOnce    sync.Once
	toolNamesCache   map[string]struct{}
)

func PublicToolSurface() ToolSurfaceInfo {
	toolSurfaceOnce.Do(func() {
		defs := compactToolDefinitions()
		toolSurfaceCache = ToolSurfaceInfo{
			Version: PublicToolSurfaceVersion,
			Hash:    toolSurfaceHashForDefinitions(defs),
			Count:   len(defs),
		}
	})
	return toolSurfaceCache
}

func publicToolContractHash() string {
	return toolContractHashForDefinitions(compactToolDefinitions(), publicMCPInstructions())
}

func currentPublicToolNames() map[string]struct{} {
	toolNamesOnce.Do(func() {
		toolNamesCache = make(map[string]struct{}, len(compactToolDefinitions()))
		for _, def := range compactToolDefinitions() {
			toolNamesCache[def.Name] = struct{}{}
		}
	})
	return toolNamesCache
}

func toolSurfaceSummary() string {
	surface := PublicToolSurface()
	return fmt.Sprintf("CodeLocal tool surface v%d (%d tools, sha256:%s)", surface.Version, surface.Count, surface.Hash)
}

// sessionlessWorkspaceRoutingInstructions explains the default-workspace rule
// to the model.
//
// A cacheable/sessionless host such as ChatGPT web cannot carry a workspace
// route between calls, so without this the model either guesses a workspaceKey
// (and fails) or asks the user a question it could have answered itself. The
// rule is appended only to the current generation's instructions: the pinned
// legacy instruction text stays byte-identical so published contract hashes for
// older generations do not drift.
const sessionlessWorkspaceRoutingInstructions = `Workspace routing: when the project is known, pass workspaceKey explicitly; explicit workspaceKey always wins, followed by the current stateful session selection. If neither exists, CodeLocal may use a user-saved default workspace; otherwise it auto-selects only when intent is unambiguous (exactly one active operator workspace, or one sleeping workspace when none is active). CodeLocal never chooses between multiple projects using recency/LastSeenAt and never implicitly routes to managed system projects. When selectionRequired is returned, call workspace(action=list) to inspect candidates and ask the user which workspace to use. Call workspace(action=select,key=...) for the current conversation; set makeDefault=true only when the user explicitly wants that choice remembered across future sessions. Workspace routing never changes the workspace access/approval mode.`

const executionModeChoiceInstructions = `Execution choice: before the first coding mutation in a workspace, call workspace(action=execution,workspaceKey=...). If configured=false, Safe Workspace remains the non-destructive default but ask the user once to choose Safe Workspace (isolated checkout, recommended) or Live Project (edit the active checkout directly). Persist only the user's explicit choice with workspace(action=execution,executionMode=safe|live,...). Never auto-switch execution mode. When configured=true, reuse the saved workspace choice without asking again.`

const agentContinuationInstructions = `Agent completion: an agent result with status=needs_continuation is a checkpoint, not a finished user task. Use its workspaceKey, objective, nextAction and fresh context to plan the next safe bounded call; continue in the current turn while useful work remains. status=ready means the quality gate passed. If status=halted or replan_required, inspect the reason and resume only when safe; never retry a denied action or replay a mutation without fresh evidence. When the AI host ends its own turn before the task is done, report the unfinished work and a concrete next step instead of claiming completion. The MCP host controls its own turn length, and CodeLocal never auto-approves tool actions.`

func publicMCPInstructions() string {
	return compactOrchestrationInstructions + "\n\n" + sessionlessWorkspaceRoutingInstructions + "\n\n" + executionModeChoiceInstructions + "\n\n" + agentContinuationInstructions + "\n\nCompatibility: " + toolSurfaceSummary() + ". Legacy tool calls that CodeLocal can translate remain supported without user action. Only CODELOCAL_TOOL_SCHEMA_MISMATCH means the client requested a contract CodeLocal cannot translate."
}

func staleToolSchemaNotice(originalTool string) string {
	surface := PublicToolSurface()
	tool := strings.TrimSpace(originalTool)
	if tool == "" {
		tool = "an older tool"
	}
	return strings.Join([]string{
		"[CODELOCAL_TOOL_SCHEMA_STALE]",
		fmt.Sprintf("The MCP client called legacy CodeLocal tool %q; CodeLocal translated it for compatibility.", tool),
		fmt.Sprintf("Current tool surface: v%d, %d tools, sha256:%s", surface.Version, surface.Count, surface.Hash),
		"Compatibility translation succeeded. Continue the workflow normally; no reconnect, refresh, new chat, or local client update is required for this request.",
		"[/CODELOCAL_TOOL_SCHEMA_STALE]",
	}, "\n")
}

func unknownToolSchemaMessage(tool string) string {
	return fmt.Sprintf("Unknown CodeLocal MCP tool %q. %s. This usually means the current MCP session has a stale or mismatched schema. Reconnect or refresh CodeLocal in the current AI client; if the local runtime is also outdated, run `npm i -g codelocal@latest` and then `codelocal`.", strings.TrimSpace(tool), toolSurfaceSummary())
}

type toolSchemaMismatchError struct{ message string }

func (e *toolSchemaMismatchError) Error() string { return e.message }

func unsupportedActionSchemaError(action string) error {
	return &toolSchemaMismatchError{message: fmt.Sprintf("unsupported action %q. %s. If the MCP client selected this action from a cached schema, reconnect or refresh CodeLocal in the current AI client", strings.TrimSpace(action), toolSurfaceSummary())}
}

func appendCompatibilityNotice(result *mcp.CallToolResult, notice string) *mcp.CallToolResult {
	if result == nil || strings.TrimSpace(notice) == "" {
		return result
	}
	result.Content = append([]mcp.Content{&mcp.TextContent{Text: notice}}, result.Content...)
	if root, ok := result.StructuredContent.(map[string]any); ok && root != nil {
		root["codeLocalCompatibility"] = map[string]any{"toolSurface": PublicToolSurface(), "translated": true, "reconnectRecommended": false}
		result.StructuredContent = root
	}
	return result
}
