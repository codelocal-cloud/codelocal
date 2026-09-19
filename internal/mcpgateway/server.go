package mcpgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/clientupdate"
	"github.com/0xmarkhydra/codelocal/internal/cloud"
	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
	"github.com/0xmarkhydra/codelocal/internal/gateway"
	"github.com/0xmarkhydra/codelocal/internal/oauth"
	usagecalc "github.com/0xmarkhydra/codelocal/internal/usage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Service struct {
	Store                *cloud.Store
	Hub                  *gateway.Hub
	Workspaces           *gateway.WorkspaceService
	Memory               longTermMemoryStore
	BlogMedia            BlogMediaImporter
	Release              clientupdate.Manifest
	Decision             *decision.Engine
	DecisionMode         decisionruntime.Mode
	DecisionProvider     string
	DecisionPrimaryReady bool
	mu                   sync.Mutex
	servers              map[string]*mcp.Server
	routes               map[string]map[string]string
	shownUpdates         map[string]map[string]struct{}

	semanticCanaryMu    sync.Mutex
	semanticCanaryGates map[string]semanticCanaryGateEntry
}

func New(store *cloud.Store, hub *gateway.Hub, workspaces *gateway.WorkspaceService, memories ...longTermMemoryStore) *Service {
	var memoryStore longTermMemoryStore
	if len(memories) > 0 {
		memoryStore = memories[0]
	}
	decisionRuntime := decisionruntime.FromEnv()
	return &Service{
		Store:                store,
		Hub:                  hub,
		Workspaces:           workspaces,
		Memory:               memoryStore,
		Release:              clientupdate.ManifestFromEnv(),
		Decision:             decisionRuntime.Engine,
		DecisionMode:         decisionRuntime.Mode,
		DecisionProvider:     decisionRuntime.Provider,
		DecisionPrimaryReady: decisionRuntime.PrimaryReady,
		servers:              map[string]*mcp.Server{},
		routes:               map[string]map[string]string{},
		shownUpdates:         map[string]map[string]struct{}{},
		semanticCanaryGates:  map[string]semanticCanaryGateEntry{},
	}
}

func objectSchema(properties map[string]any, required ...string) json.RawMessage {
	value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		value["required"] = required
	}
	raw, _ := json.Marshal(value)
	return raw
}
func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func boolean(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
func integer(description string, min, max int) map[string]any {
	out := map[string]any{"type": "integer", "description": description}
	if min != 0 {
		out["minimum"] = min
	}
	if max != 0 {
		out["maximum"] = max
	}
	return out
}
func array(items any, description string) map[string]any {
	return map[string]any{"type": "array", "items": items, "description": description}
}
func anyObject(description string) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true, "description": description}
}

var workspaceKeySchema = map[string]any{"type": "string", "minLength": 1, "description": "Exact workspace key returned by workspace(action=list/select). Pass it to keep routing explicit across multiple active projects, AI client conversations or fresh MCP sessions."}

func boolPtr(value bool) *bool { return &value }
func protocolOneRuntimeTool(name string) bool {
	switch name {
	case "project_info", "read_instructions", "list_files", "file_info", "read_file", "read_file_range", "read_files", "search_code", "inspect_dependency", "read_dependency", "search_dependency", "find_symbol", "find_definition", "find_references", "get_callers", "get_callees", "get_import_graph", "get_diagnostics", "write_file", "edit_file", "apply_patch", "git_status", "git_diff", "git_log", "git_show", "git_blame", "git_file_history", "run_command", "process_poll", "process_list", "process_write", "process_kill":
		return true
	default:
		return false
	}
}

func capabilityBool(capabilities map[string]any, name string) bool {
	value, _ := capabilities[name].(bool)
	return value
}

func ensureOperationSupported(operation operationInvocation, workspace *gateway.WorkspaceView, optionalArgs ...map[string]any) error {
	if workspace == nil {
		return errors.New("workspace unavailable")
	}
	if automationRuntimeTool(operation.RuntimeTool) {
		return ensureAutomationOperationSupported(operation.RuntimeTool, workspace, optionalArgs...)
	}
	if operation.RuntimeTool == "approval_mode" && workspace.ProtocolVersion < 3 {
		return fmt.Errorf("%s requires CodeLocal protocol v3 or newer; update the client before changing chat access mode", operation.OperationID)
	}
	if workspace.ProtocolVersion <= 1 {
		if !protocolOneRuntimeTool(operation.RuntimeTool) {
			return fmt.Errorf("%s requires a newer CodeLocal client; update the client before using this operation", operation.OperationID)
		}
		return nil
	}
	capability := operation.Capability
	if capability == "filesystem" && (operation.RuntimeTool == "sandbox_info" || operation.RuntimeTool == "sandbox_smoke_test") {
		return nil
	}
	if capability == "pty" {
		if !capabilityBool(workspace.Capabilities, "shell") || !capabilityBool(workspace.Capabilities, "pty") {
			return fmt.Errorf("%s is unavailable because this CodeLocal client does not advertise PTY support", operation.OperationID)
		}
		return nil
	}
	if !capabilityBool(workspace.Capabilities, capability) {
		return fmt.Errorf("%s is unavailable because this CodeLocal client does not advertise %s support", operation.OperationID, capability)
	}
	return nil
}

const modernMCPProtocolVersion = "2026-07-28"

func isModernMCPProtocolVersion(version string) bool {
	version = strings.TrimSpace(version)
	return len(version) == len(modernMCPProtocolVersion) && version >= modernMCPProtocolVersion
}

func requestUsesModernMCP(r *http.Request) bool {
	if r == nil {
		return false
	}
	// MCP 2026-07-28 is sessionless/stateless. Classify an explicit modern
	// protocol header before looking at the request method/body. The SDK will
	// correctly reject unsupported methods (for example standalone GET in the
	// 2026 protocol) rather than accidentally routing them into a legacy session.
	if isModernMCPProtocolVersion(r.Header.Get("Mcp-Protocol-Version")) {
		return true
	}
	if r.Method != http.MethodPost || r.Body == nil {
		return false
	}
	const probeLimit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(r.Body, probeLimit+1))
	if len(raw) > 0 {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
	}
	if err != nil || len(raw) > probeLimit {
		return false
	}
	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	if envelope.Method == "server/discover" {
		return true
	}
	version, _ := envelope.Params.Meta[mcp.MetaKeyProtocolVersion].(string)
	return isModernMCPProtocolVersion(version)
}

func hybridMCPTransport(stateful, stateless http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestUsesModernMCP(r) {
			w.Header().Set("X-CodeLocal-MCP-Transport", "stateless-2026")
			stateless.ServeHTTP(w, r)
			return
		}
		w.Header().Set("X-CodeLocal-MCP-Transport", "stateful-legacy")
		stateful.ServeHTTP(w, r)
	})
}

func streamableMCPHandler(getServer func(*http.Request) *mcp.Server) http.Handler {
	stateful := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless:           false,
		JSONResponse:        true,
		MaxRequestBodyBytes: 4 << 20,
	})
	stateless := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          4 << 20,
		PropagateRequestCancellation: true,
	})
	return hybridMCPTransport(stateful, stateless)
}

func (s *Service) Handler() http.Handler {
	stream := streamableMCPHandler(func(r *http.Request) *mcp.Server {
		claims, ok := oauth.ClaimsFrom(r.Context())
		if !ok || claims.Subject == "" {
			return nil
		}
		return s.serverFor(claims.Subject)
	})
	surface := PublicToolSurface()
	withSurface := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CodeLocal-Tool-Surface-Version", fmt.Sprint(surface.Version))
		w.Header().Set("X-CodeLocal-Tool-Surface-Hash", surface.Hash)
		stream.ServeHTTP(w, r)
	})
	return withSurface
}

func (s *Service) ToolSurface() ToolSurfaceInfo { return PublicToolSurface() }

func (s *Service) serverFor(userID string) *mcp.Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.servers[userID]; existing != nil {
		return existing
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "codelocal", Version: PublicMCPImplementationVersion}, &mcp.ServerOptions{Instructions: publicMCPInstructions()})
	registerCompactTools(server, s, userID)
	s.servers[userID] = server
	return server
}

func sessionID(req *mcp.CallToolRequest) string {
	if req != nil && req.Session != nil {
		if id := strings.TrimSpace(req.Session.ID()); id != "" {
			return id
		}
	}
	return "stateless"
}
func decodeArgs(req *mcp.CallToolRequest) (map[string]any, error) {
	args := map[string]any{}
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return args, nil
	}
	if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
		return nil, err
	}
	return args, nil
}
func textResultWithNotice(value any, isError bool, notice string) *mcp.CallToolResult {
	var text string
	structured := map[string]any{}
	if raw, err := json.Marshal(value); err == nil {
		text = string(raw)
		// MCP structuredContent is object-shaped. Runtime operations such as
		// computer_list_windows legitimately return a top-level array, so keep the
		// compatibility text unchanged but wrap non-object JSON values for clients
		// that validate structuredContent strictly.
		var normalized any
		if json.Unmarshal(raw, &normalized) == nil {
			if root, ok := normalized.(map[string]any); ok {
				structured = root
			} else {
				structured["result"] = normalized
			}
		}
	} else {
		text = fmt.Sprint(value)
		structured["result"] = text
	}
	structured["codeLocalToolSurface"] = PublicToolSurface()
	if strings.TrimSpace(notice) != "" {
		text = notice + "\n\n" + text
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: structured, IsError: isError}
}
func textResult(value any, isError bool) *mcp.CallToolResult {
	return textResultWithNotice(value, isError, "")
}
func errorResult(err error) *mcp.CallToolResult {
	return textResult(map[string]any{"error": err.Error()}, true)
}

const (
	codeLocalDeviceOffline           = "CODELOCAL_DEVICE_OFFLINE"
	codeLocalWorkspaceUnauthorized   = "CODELOCAL_WORKSPACE_UNAUTHORIZED"
	codeLocalWorkspaceUnavailable    = "CODELOCAL_WORKSPACE_UNAVAILABLE"
	codeLocalWorkspaceNotSelected    = "CODELOCAL_WORKSPACE_NOT_SELECTED"
	codeLocalTransientRoutingFailure = "CODELOCAL_TRANSIENT_ROUTING_FAILURE"
	codeLocalRuntimeFailure          = "CODELOCAL_RUNTIME_FAILURE"
)

func codedErrorResult(code string, err error, retryable bool, details map[string]any) *mcp.CallToolResult {
	payload := map[string]any{"code": code, "error": err.Error(), "retryable": retryable}
	for key, value := range details {
		payload[key] = value
	}
	return textResult(payload, true)
}

func classifyGatewayFailure(err error, runtimeCode string) (string, bool) {
	if runtimeCode == "CLIENT_OFFLINE" {
		return codeLocalTransientRoutingFailure, true
	}
	if err == nil {
		return codeLocalRuntimeFailure, false
	}
	var activationFailure *gateway.WorkspaceActivationFailure
	if errors.As(err, &activationFailure) {
		return codeLocalTransientRoutingFailure, true
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "no active workspace"), strings.Contains(message, "multiple workspaces are active"), strings.Contains(message, "no workspace selected"):
		return codeLocalWorkspaceNotSelected, false
	case strings.Contains(message, "not authorized"), strings.Contains(message, "no longer authorized"):
		return codeLocalWorkspaceUnauthorized, false
	case strings.Contains(message, "device for") && strings.Contains(message, "offline"):
		return codeLocalDeviceOffline, false
	case strings.Contains(message, "workspace is not available"):
		return codeLocalWorkspaceUnavailable, false
	case strings.Contains(message, "workspace activation timed out"),
		strings.Contains(message, "workspace is offline"),
		strings.Contains(message, "gateway coordinator closed"),
		strings.Contains(message, "client disconnected"),
		strings.Contains(message, "connection is not owned by this gateway"):
		return codeLocalTransientRoutingFailure, true
	default:
		return codeLocalRuntimeFailure, false
	}
}

func routeRetryAllowed(operation operationInvocation, workspace *gateway.WorkspaceView) bool {
	if !operation.SideEffecting || operation.Idempotent {
		return true
	}
	return workspace != nil && workspace.ProtocolVersion >= 2 && capabilityBool(workspace.Capabilities, "idempotency")
}

func transientRouteFailure(result gateway.RoutedResult, err error) bool {
	code, retryable := classifyGatewayFailure(err, result.ErrorCode)
	return code == codeLocalTransientRoutingFailure && retryable
}

func gatewayFailureResult(err error, runtimeCode, requestID, workspaceKey string, retryCount int, rebound bool) *mcp.CallToolResult {
	code, retryable := classifyGatewayFailure(err, runtimeCode)
	details := map[string]any{
		"requestId": requestID, "workspaceKey": workspaceKey, "retryCount": retryCount, "rebound": rebound,
	}
	var activationFailure *gateway.WorkspaceActivationFailure
	if errors.As(err, &activationFailure) {
		for key, value := range activationFailure.Details() {
			details[key] = value
		}
	}
	return codedErrorResult(code, err, retryable, details)
}

func activeWorkspaceKeyForOperation(active []gateway.WorkspaceView, _ operationInvocation) (string, bool) {
	if len(active) == 1 {
		return active[0].Key, true
	}
	// Never choose between multiple workspaces by recency, even for
	// device-oriented tools such as Computer Use. Workspace access mode and
	// approvals are workspace-scoped, so two projects on the same device are
	// still an authorization ambiguity that requires an explicit/default route.
	return "", false
}

func attachWorkspaceHandle(result *mcp.CallToolResult, workspace *gateway.WorkspaceView) *mcp.CallToolResult {
	if result == nil || workspace == nil {
		return result
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		structured = map[string]any{"result": result.StructuredContent}
		result.StructuredContent = structured
	}
	structured["workspaceKey"] = workspace.Key
	structured["deviceId"] = workspace.DeviceID
	structured["workspaceId"] = workspace.WorkspaceID
	return result
}

func (s *Service) route(userID, session string) string {
	if strings.TrimSpace(session) == "" || session == "stateless" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routes[userID][session]
}
func (s *Service) setRoute(userID, session, key string) {
	if strings.TrimSpace(session) == "" || session == "stateless" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routes[userID] == nil {
		s.routes[userID] = map[string]string{}
	}
	s.routes[userID][session] = key
}

func (s *Service) claimUpdate(userID, session, workspaceKey, installedVersion string) string {
	notice := clientupdate.Evaluate(installedVersion, s.Release)
	if notice == nil {
		return ""
	}
	sessionKey := userID + ":" + session
	claimKey := workspaceKey + ":" + notice.Key
	s.mu.Lock()
	if s.shownUpdates[sessionKey] == nil {
		s.shownUpdates[sessionKey] = map[string]struct{}{}
	}
	if _, shown := s.shownUpdates[sessionKey][claimKey]; shown {
		s.mu.Unlock()
		return ""
	}
	s.shownUpdates[sessionKey][claimKey] = struct{}{}
	s.mu.Unlock()
	return clientupdate.Render(*notice)
}

func (s *Service) firstUpdateNotice(userID, session string, workspaces []gateway.WorkspaceView) string {
	for _, workspace := range workspaces {
		if notice := s.claimUpdate(userID, session, workspace.Key, workspace.ClientVersion); notice != "" {
			return notice
		}
	}
	return ""
}

func (s *Service) callOperation(ctx context.Context, userID, publicTool string, operation operationInvocation, args map[string]any, req *mcp.CallToolRequest) (response *mcp.CallToolResult, retErr error) {
	startedAt := time.Now()
	session := sessionID(req)
	inputBytes, inputTokens := usagecalc.EstimateTokens(args)
	usageDeviceID := ""
	usageWorkspaceID := ""
	defer func() {
		if response == nil || s.Store == nil {
			return
		}
		outputBytes, outputTokens := usagecalc.EstimateTokens(response.Content)
		event := cloud.MCPUsageEvent{UserID: userID, SessionID: session, DeviceID: usageDeviceID, WorkspaceID: usageWorkspaceID, Tool: publicTool, InputBytes: inputBytes, OutputBytes: outputBytes, InputTokensEst: inputTokens, OutputTokensEst: outputTokens, CreatedAt: time.Now().UnixMilli()}
		// Usage is telemetry, so request latency must never depend on Redis or
		// PostgreSQL. RecordMCPUsage only offers the event to a bounded local
		// queue; background workers publish it durably and persist it in order.
		_ = s.Store.RecordMCPUsage(context.Background(), event)
	}()
	if operation.Local {
		return s.callLocal(ctx, userID, session, operation.RuntimeTool, args)
	}
	explicit, _ := args["workspaceKey"].(string)
	delete(args, "workspaceKey")
	key := strings.TrimSpace(explicit)
	if key == "" {
		key = s.route(userID, session)
	}
	if key == "" {
		key = s.defaultWorkspaceKey(ctx, userID)
	}
	if key == "" {
		catalog, catalogErr := s.Workspaces.Catalog(ctx, userID)
		if catalogErr != nil {
			return gatewayFailureResult(catalogErr, "", "", "", 0, false), nil
		}
		active := []gateway.WorkspaceView{}
		for _, w := range catalog {
			if w.Status == "active" {
				active = append(active, w)
			}
		}
		if inferred, ok := activeWorkspaceKeyForOperation(active, operation); ok {
			key = inferred
		} else {
			routable := 0
			for _, workspace := range catalog {
				if workspaceImplicitRoutable(workspace) {
					routable++
				}
			}
			if routable > 0 {
				return workspaceSelectionRequiredResult(catalog), nil
			}
			err := workspaceRoutingError(false)
			return gatewayFailureResult(err, "", "", "", 0, false), nil
		}
	}
	workspace, err := s.Workspaces.Activate(ctx, userID, key)
	if err != nil {
		return gatewayFailureResult(err, "", "", key, 0, false), nil
	}
	usageDeviceID = workspace.DeviceID
	usageWorkspaceID = workspace.WorkspaceID
	if err := ensureOperationSupported(operation, workspace, args); err != nil {
		compatibility := map[string]any{
			"error":                  err.Error(),
			"code":                   "CODELOCAL_OPERATION_UNSUPPORTED",
			"installedClientVersion": workspace.ClientVersion,
			"protocolVersion":        workspace.ProtocolVersion,
			"toolSurface":            PublicToolSurface(),
		}
		if update := clientupdate.Evaluate(workspace.ClientVersion, s.Release); update != nil {
			compatibility["updateAvailable"] = true
			compatibility["latestClientVersion"] = update.LatestVersion
			compatibility["updateCommand"] = update.UpdateCommand
			compatibility["restartCommand"] = update.RestartCommand
		}
		notice := s.claimUpdate(userID, session, workspace.Key, workspace.ClientVersion)
		return textResultWithNotice(compatibility, true, notice), nil
	}
	requestID := cloud.RandomHex(16)
	result, callErr := s.Hub.Call(ctx, userID, key, session, operation.RuntimeTool, args, operation.SideEffecting, requestID)
	retryCount := 0
	rebound := false
	if transientRouteFailure(result, callErr) && routeRetryAllowed(operation, workspace) {
		retryCount = 1
		initialCode := result.ErrorCode
		if initialCode == "" && callErr != nil {
			initialCode, _ = classifyGatewayFailure(callErr, "")
		}
		slog.Warn("MCP workspace route lost; rebinding once", "requestId", requestID, "mcpSessionId", session, "workspace", key, "publicTool", publicTool, "runtimeTool", operation.RuntimeTool, "initialErrorCode", initialCode)
		if s.Store != nil {
			s.Store.Audit(cloud.AuditEvent{UserID: userID, Event: "workspace.route_rebind", DeviceID: workspace.DeviceID, WorkspaceID: workspace.WorkspaceID, Detail: map[string]any{"requestId": requestID, "mcpSessionId": session, "tool": publicTool, "runtimeTool": operation.RuntimeTool, "initialErrorCode": initialCode}})
		}
		reboundWorkspace, rebindErr := s.Workspaces.Activate(ctx, userID, key)
		if rebindErr != nil {
			callErr = rebindErr
			result = gateway.RoutedResult{}
		} else {
			workspace = reboundWorkspace
			rebound = true
			// Reuse the exact request ID and arguments. Side-effecting operations
			// are retried only when their operation is intrinsically idempotent or
			// the runtime advertises request-id idempotency, so an approval token
			// and execution identity remain bound to the exact same action.
			result, callErr = s.Hub.Call(ctx, userID, key, session, operation.RuntimeTool, args, operation.SideEffecting, requestID)
		}
	}
	totalDurationMs := time.Since(startedAt).Milliseconds()
	runtimeDurationMs := metadataInt64(result.Metadata, "runtimeDurationMs")
	relayDurationMs := totalDurationMs - runtimeDurationMs
	if relayDurationMs < 0 {
		relayDurationMs = 0
	}
	slog.Debug("MCP gateway operation completed", "requestId", requestID, "mcpSessionId", session, "publicTool", publicTool, "operationId", operation.OperationID, "runtimeTool", operation.RuntimeTool, "workspace", key, "ok", callErr == nil && result.OK, "durationMs", totalDurationMs, "runtimeDurationMs", runtimeDurationMs, "relayDurationMs", relayDurationMs, "retryCount", retryCount, "rebound", rebound)
	if callErr != nil {
		return gatewayFailureResult(callErr, "", requestID, key, retryCount, rebound), nil
	}
	if !result.OK {
		err := errors.New(firstNonEmpty(result.Error, result.ErrorCode, "tool failed"))
		return gatewayFailureResult(err, result.ErrorCode, requestID, key, retryCount, rebound), nil
	}
	if operation.TerminalExecution && s.Store != nil {
		s.Store.Audit(cloud.AuditEvent{UserID: userID, Event: "terminal.executed", DeviceID: workspace.DeviceID, WorkspaceID: workspace.WorkspaceID, Detail: map[string]any{"requestId": requestID, "tool": publicTool, "runtimeTool": operation.RuntimeTool, "operationId": operation.OperationID}})
	}
	notice := s.claimUpdate(userID, session, workspace.Key, workspace.ClientVersion)
	return attachWorkspaceHandle(toolResultWithNotice(result.Result, false, notice), workspace), nil
}

// configuredDefaultWorkspaceKey returns the user's durable default first and
// keeps the environment variable only as an operator-level fallback for
// self-hosted deployments. A stored key never bypasses workspace authorization;
// it is validated against the current catalog before use.
func (s *Service) configuredDefaultWorkspaceKey(ctx context.Context, userID string) string {
	if s != nil && s.Store != nil {
		preference, err := s.Store.WorkspaceRoutingPreference(ctx, userID)
		if err == nil && strings.TrimSpace(preference.DefaultWorkspaceKey) != "" {
			return strings.TrimSpace(preference.DefaultWorkspaceKey)
		}
		if err != nil {
			slog.Debug("workspace routing preference lookup failed", "error", err)
		}
	}
	return strings.TrimSpace(os.Getenv("CODELOCAL_DEFAULT_WORKSPACE_ID"))
}

// defaultWorkspaceKey resolves sessionless routing without guessing between
// multiple operator projects. Resolution order is explicit/session selection
// (handled by callers), then the user-saved default, then an unambiguous
// implicit choice: exactly one active workspace, or exactly one sleeping
// workspace when none is active. LastSeenAt is never used to choose between
// multiple projects.
func (s *Service) defaultWorkspaceKey(ctx context.Context, userID string) string {
	if s == nil || s.Workspaces == nil {
		return ""
	}
	catalog, err := s.Workspaces.Catalog(ctx, userID)
	if err != nil {
		return ""
	}
	return selectDefaultWorkspaceKey(catalog, s.configuredDefaultWorkspaceKey(ctx, userID))
}

func workspaceAuthorizedForRouting(workspace gateway.WorkspaceView) bool {
	if authorized, ok := workspace.Authorized.(bool); ok && !authorized {
		return false
	}
	return workspace.Status == "active" || workspace.Status == "sleeping"
}

func workspaceImplicitRoutable(workspace gateway.WorkspaceView) bool {
	if !workspaceAuthorizedForRouting(workspace) {
		return false
	}
	if workspace.System || workspace.SystemApp || workspace.Managed || strings.HasPrefix(strings.TrimSpace(workspace.WorkspaceID), "system-") {
		return false
	}
	return true
}

func configuredWorkspaceKey(catalog []gateway.WorkspaceView, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return ""
	}
	for _, workspace := range catalog {
		if workspace.WorkspaceID != configured && workspace.Key != configured {
			continue
		}
		if workspaceAuthorizedForRouting(workspace) {
			return workspace.Key
		}
	}
	return ""
}

// selectDefaultWorkspaceKey is intentionally conservative: a durable default
// wins, otherwise CodeLocal auto-routes only when the user's intent is
// unambiguous. Multiple active/sleeping projects require an explicit choice.
func selectDefaultWorkspaceKey(catalog []gateway.WorkspaceView, configured string) string {
	if key := configuredWorkspaceKey(catalog, configured); key != "" {
		return key
	}
	active := make([]gateway.WorkspaceView, 0, len(catalog))
	sleeping := make([]gateway.WorkspaceView, 0, len(catalog))
	for _, workspace := range catalog {
		if !workspaceImplicitRoutable(workspace) {
			continue
		}
		if workspace.Status == "active" {
			active = append(active, workspace)
		} else if workspace.Status == "sleeping" {
			sleeping = append(sleeping, workspace)
		}
	}
	if len(active) == 1 {
		return active[0].Key
	}
	if len(active) > 1 {
		return ""
	}
	if len(sleeping) == 1 {
		return sleeping[0].Key
	}
	return ""
}

func workspaceSelectionRequiredResult(catalog []gateway.WorkspaceView) *mcp.CallToolResult {
	candidates := make([]map[string]any, 0, len(catalog))
	for _, workspace := range catalog {
		if !workspaceImplicitRoutable(workspace) {
			continue
		}
		candidates = append(candidates, map[string]any{
			"key": workspace.Key, "workspaceId": workspace.WorkspaceID, "workspaceName": workspace.WorkspaceName,
			"projectId": workspace.ProjectID, "projectName": workspace.ProjectName, "deviceId": workspace.DeviceID,
			"deviceName": workspace.DeviceName, "status": workspace.Status,
		})
	}
	err := errors.New("multiple CodeLocal workspaces are available; ask the user which project to use instead of guessing")
	return codedErrorResult(codeLocalWorkspaceNotSelected, err, false, map[string]any{
		"selectionRequired": true,
		"workspaces":        candidates,
		"canSetDefault":     true,
		"nextAction":        "Ask the user to choose a workspace. Use workspace(action=select,key=...) for this conversation, or makeDefault=true only when the user wants it remembered.",
	})
}

func workspaceRoutingError(multiple bool) error {
	if multiple {
		return errors.New("multiple workspaces are active; call workspace(action=select), and pass workspaceKey for explicit routing")
	}
	return errors.New("no active workspace; call workspace(action=list) then workspace(action=select)")
}

func metadataInt64(metadata any, key string) int64 {
	values, ok := metadata.(map[string]any)
	if !ok {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func (s *Service) callLocal(ctx context.Context, userID, session, tool string, args map[string]any) (*mcp.CallToolResult, error) {
	switch tool {
	case "list_devices":
		clients := s.Hub.LocalClients(userID)
		groups := map[string][]map[string]any{}
		for _, c := range clients {
			groups[c.DeviceID] = append(groups[c.DeviceID], map[string]any{"workspaceId": c.WorkspaceID, "workspaceName": c.WorkspaceName, "key": c.Key, "projectRoot": c.ProjectRoot, "protocolVersion": c.ProtocolVersion, "clientVersion": c.ClientVersion, "capabilities": c.Capabilities, "lastSeenAt": c.LastSeenAt()})
		}
		devices := []map[string]any{}
		keys := []string{}
		for key := range groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			devices = append(devices, map[string]any{"deviceId": key, "workspaces": groups[key]})
		}
		notice := ""
		for _, client := range clients {
			if notice = s.claimUpdate(userID, session, client.Key, client.ClientVersion); notice != "" {
				break
			}
		}
		return textResultWithNotice(map[string]any{"devices": devices}, false, notice), nil
	case "list_device_identities":
		devices, err := s.Store.ListDevices(ctx, userID)
		if err != nil {
			return errorResult(err), nil
		}
		for i := range devices {
			devices[i].SecretHash = ""
		}
		return textResult(devices, false), nil
	case "revoke_device":
		id, _ := args["credentialId"].(string)
		if id == "" {
			return errorResult(errors.New("credentialId required")), nil
		}
		ok, err := s.Store.RevokeDevice(ctx, userID, id)
		if err != nil {
			return errorResult(err), nil
		}
		return textResult(map[string]any{"revoked": ok}, false), nil
	case "rename_device":
		id, _ := args["credentialId"].(string)
		name, _ := args["deviceName"].(string)
		ok, err := s.Store.RenameDevice(ctx, userID, id, strings.TrimSpace(name))
		if err != nil {
			return errorResult(err), nil
		}
		return textResult(map[string]any{"renamed": ok}, false), nil
	case "list_workspaces":
		catalog, err := s.Workspaces.Catalog(ctx, userID)
		if err != nil {
			return errorResult(err), nil
		}
		notice := s.firstUpdateNotice(userID, session, catalog)
		configured := configuredWorkspaceKey(catalog, s.configuredDefaultWorkspaceKey(ctx, userID))
		return textResultWithNotice(map[string]any{"selectedWorkspace": s.route(userID, session), "defaultWorkspace": configured, "workspaces": catalog}, false, notice), nil
	case "select_workspace":
		key, _ := args["key"].(string)
		workspace, err := s.Workspaces.Activate(ctx, userID, key)
		if err != nil {
			return gatewayFailureResult(err, "", "", key, 0, false), nil
		}
		makeDefault, _ := args["makeDefault"].(bool)
		if makeDefault {
			if s.Store == nil {
				return errorResult(errors.New("workspace default store unavailable")), nil
			}
			if err := s.Store.SetDefaultWorkspaceKey(ctx, userID, workspace.Key); err != nil {
				return errorResult(err), nil
			}
		}
		s.setRoute(userID, session, key)
		notice := s.claimUpdate(userID, session, workspace.Key, workspace.ClientVersion)
		payload := map[string]any{"selected": key, "workspaceKey": key, "deviceId": workspace.DeviceID, "workspaceId": workspace.WorkspaceID, "workspaceName": workspace.WorkspaceName, "clientVersion": workspace.ClientVersion, "status": "active"}
		if makeDefault {
			payload["defaultWorkspace"] = workspace.Key
			payload["defaultSaved"] = true
		}
		return textResultWithNotice(payload, false, notice), nil
	case "workspace_info":
		key, _ := args["workspaceKey"].(string)
		if strings.TrimSpace(key) == "" {
			key = s.route(userID, session)
		}
		if strings.TrimSpace(key) == "" {
			key = s.defaultWorkspaceKey(ctx, userID)
		}
		if key == "" {
			err := errors.New("no workspace selected")
			return gatewayFailureResult(err, "", "", "", 0, false), nil
		}
		workspace, err := s.Workspaces.Activate(ctx, userID, key)
		if err != nil {
			return gatewayFailureResult(err, "", "", key, 0, false), nil
		}
		notice := s.claimUpdate(userID, session, workspace.Key, workspace.ClientVersion)
		return textResultWithNotice(workspace, false, notice), nil
	case "execution_mode":
		return s.callExecutionMode(ctx, userID, session, args)
	case "memory_remember":
		return s.rememberConversationMemory(ctx, userID, session, args)
	case "memory_recall":
		return s.recallConversationMemory(ctx, userID, session, args)
	default:
		return errorResult(errors.New("unknown local MCP tool")), nil
	}
}
