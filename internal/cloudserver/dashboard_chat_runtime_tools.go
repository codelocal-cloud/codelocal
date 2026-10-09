package cloudserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
	"github.com/0xmarkhydra/codelocal/internal/gateway"
)

func dashboardPluginArgString(value any) string {
	text, _ := value.(string)
	return text
}

type dashboardExecutionState struct {
	workspace       *gateway.WorkspaceView
	sessionID       string
	requestID       string
	toolOccurrences map[string]int
	activeCommands  map[string]string
	resume          dashboardChatExecutionResume
}

type dashboardExecutionStateKey struct{}

func dashboardWithExecutionState(r *http.Request, userID, requestID string, workspace *gateway.WorkspaceView) *http.Request {
	requestID = strings.TrimSpace(requestID)
	sessionSuffix := requestID
	if sessionSuffix == "" {
		sessionSuffix = cloud.RandomHex(8)
	}
	state := &dashboardExecutionState{
		workspace:       workspace,
		sessionID:       "dashboard-" + userID + "-" + sessionSuffix,
		requestID:       requestID,
		toolOccurrences: map[string]int{},
		activeCommands:  map[string]string{},
	}
	return r.WithContext(context.WithValue(r.Context(), dashboardExecutionStateKey{}, state))
}

func dashboardExecutionStateFromRequest(r *http.Request) *dashboardExecutionState {
	state, _ := r.Context().Value(dashboardExecutionStateKey{}).(*dashboardExecutionState)
	return state
}

func dashboardSetExecutionWorkspace(r *http.Request, workspace *gateway.WorkspaceView) {
	if state := dashboardExecutionStateFromRequest(r); state != nil {
		state.workspace = workspace
	}
}

func dashboardSetExecutionResume(r *http.Request, resume dashboardChatExecutionResume) {
	if state := dashboardExecutionStateFromRequest(r); state != nil {
		state.resume = resume
		state.activeCommands = map[string]string{}
		dashboardSeedActiveCommands(state, resume.Results)
	}
}

func dashboardExecutionResumeFromRequest(r *http.Request) dashboardChatExecutionResume {
	if state := dashboardExecutionStateFromRequest(r); state != nil {
		return state.resume
	}
	return dashboardChatExecutionResume{}
}

var dashboardRuntimeChatTools = []map[string]any{
	dashboardRuntimeTool("get_project_git_status", "Inspect the current Git status in the active project. Read-only; use before editing or reviewing changes.", map[string]any{
		"workspace":  map[string]any{"type": "string", "description": "Optional active workspace name or id."},
		"repository": map[string]any{"type": "string", "description": "Optional repository identifier for multi-repo workspaces."},
	}),
	dashboardRuntimeTool("review_project_diff", "Read the actual Git unified diff from the active project. Read-only; never fabricate changes.", map[string]any{
		"workspace":  map[string]any{"type": "string", "description": "Optional active workspace name or id."},
		"repository": map[string]any{"type": "string", "description": "Optional repository identifier for multi-repo workspaces."},
		"path":       map[string]any{"type": "string", "description": "Optional workspace-relative file path."},
		"cached":     map[string]any{"type": "boolean", "description": "Include staged changes only when true."},
	}),
	dashboardRuntimeTool("list_project_files", "List files in the active CodeLocal project runtime.", map[string]any{
		"workspace": map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"path":      map[string]any{"type": "string", "description": "Optional workspace-relative directory."},
		"maxDepth":  map[string]any{"type": "integer"},
	}),
	dashboardRuntimeTool("read_project_file", "Read a file from the active CodeLocal project runtime. Use this before editing code.", map[string]any{
		"workspace": map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"path":      map[string]any{"type": "string"},
		"startLine": map[string]any{"type": "integer"},
		"endLine":   map[string]any{"type": "integer"},
	}, "path"),
	dashboardRuntimeTool("search_project_code", "Search text in the active CodeLocal project runtime.", map[string]any{
		"workspace":  map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"query":      map[string]any{"type": "string"},
		"path":       map[string]any{"type": "string"},
		"maxResults": map[string]any{"type": "integer"},
	}, "query"),
	dashboardRuntimeTool("edit_project_file", "Edit an existing project file by exact replacement. This executes on the user's CodeLocal runtime.", map[string]any{
		"workspace":    map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"path":         map[string]any{"type": "string"},
		"oldText":      map[string]any{"type": "string"},
		"newText":      map[string]any{"type": "string"},
		"replaceAll":   map[string]any{"type": "boolean"},
		"expectedHash": map[string]any{"type": "string"},
	}, "path", "oldText", "newText"),
	dashboardRuntimeTool("write_project_file", "Create or replace a project file on the user's CodeLocal runtime.", map[string]any{
		"workspace": map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"path":      map[string]any{"type": "string"},
		"content":   map[string]any{"type": "string"},
	}, "path", "content"),
	dashboardRuntimeTool("apply_project_patch", "Apply a unified Git patch to the active CodeLocal project runtime.", map[string]any{
		"workspace": map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"patch":     map[string]any{"type": "string"},
	}, "patch"),
	dashboardRuntimeTool("run_project_command", "Start a guarded shell command and wait for its first result. Use for tests, builds, linters and project commands. If it returns running=true, the process already exists: call poll_project_command with its processId and never launch the same command again.", map[string]any{
		"workspace": map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"command":   map[string]any{"type": "string"},
		"cwd":       map[string]any{"type": "string"},
		"timeoutMs": map[string]any{"type": "integer"},
	}, "command"),
	dashboardRuntimeTool("poll_project_command", "Wait for an already-running project command by processId. Keep polling this process; never start the same command again while it is running.", map[string]any{
		"workspace": map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"processId": map[string]any{"type": "string"},
		"cursor":    map[string]any{"type": "integer"},
	}, "processId"),
	dashboardRuntimeTool("verify_project_changes", "Verify edits through the CodeLocal runtime and return diagnostics/diff checks.", map[string]any{
		"workspace": map[string]any{"type": "string", "description": "Optional workspace/project name or id."},
		"paths":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}),
}

func dashboardRuntimeTool(name, description string, properties map[string]any, required ...string) map[string]any {
	parameters := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		parameters["required"] = required
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": parameters}}
}

func dashboardRuntimeToolSpec(name string, args map[string]any) (runtimeTool string, sideEffect bool, forward map[string]any, ok bool) {
	forward = make(map[string]any, len(args))
	for key, value := range args {
		if key != "workspace" {
			forward[key] = value
		}
	}
	switch name {
	case "get_project_git_status":
		return "git_status", false, forward, true
	case "review_project_diff":
		return "git_diff", false, forward, true
	case "list_project_files":
		return "list_files", false, forward, true
	case "read_project_file":
		if _, hasStart := forward["startLine"]; hasStart {
			if _, hasEnd := forward["endLine"]; hasEnd {
				return "read_file_range", false, forward, true
			}
		}
		delete(forward, "startLine")
		delete(forward, "endLine")
		return "read_file", false, forward, true
	case "search_project_code":
		return "search_code", false, forward, true
	case "edit_project_file":
		return "edit_file", true, forward, true
	case "write_project_file":
		return "write_file", true, forward, true
	case "apply_project_patch":
		return "apply_patch", true, forward, true
	case "run_project_command":
		return "run_command", true, forward, true
	case "poll_project_command":
		return "process_poll", false, forward, true
	case "verify_project_changes":
		return "verify_changes", false, forward, true
	default:
		return "", false, nil, false
	}
}

func dashboardResolveRuntimeWorkspace(r *http.Request, s *Server, userID string, args map[string]any) (*gateway.WorkspaceView, error) {
	// A task bound to a project must never silently hop to another workspace
	// because the LLM provided a different optional tool argument.
	if state := dashboardExecutionStateFromRequest(r); state != nil && state.workspace != nil {
		requested, _ := args["workspace"].(string)
		requested = strings.TrimSpace(requested)
		if requested != "" && dashboardChatFindWorkspace([]gateway.WorkspaceView{*state.workspace}, requested) == nil {
			return nil, fmt.Errorf("workspace locked to active task; start a new task to change project")
		}
		return state.workspace, nil
	}
	if s.Workspaces == nil {
		return nil, fmt.Errorf("workspace service unavailable")
	}
	if requested, _ := args["workspace"].(string); strings.TrimSpace(requested) != "" {
		catalog, err := s.Workspaces.Catalog(r.Context(), userID)
		if err != nil {
			return nil, err
		}
		workspace := dashboardChatFindWorkspace(catalog, requested)
		if workspace == nil {
			return nil, fmt.Errorf("workspace not found: %s", requested)
		}
		active, err := dashboardChatActivateWorkspace(r.Context(), s, userID, workspace)
		if err != nil {
			return nil, err
		}
		dashboardSetExecutionWorkspace(r, active)
		return active, nil
	}
	if state := dashboardExecutionStateFromRequest(r); state != nil && state.workspace != nil {
		return state.workspace, nil
	}
	catalog, err := s.Workspaces.Catalog(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	if current := dashboardChatCurrentWorkspace(catalog); current != nil {
		dashboardSetExecutionWorkspace(r, current)
		return current, nil
	}
	return nil, fmt.Errorf("no active project context; call get_workspace_detail first or provide workspace")
}

func dashboardRuntimeRequestID(state *dashboardExecutionState, runtimeTool string, args map[string]any) string {
	if state == nil || strings.TrimSpace(state.requestID) == "" {
		return cloud.RandomHex(16)
	}
	encoded, _ := json.Marshal(args)
	digest := sha256.Sum256([]byte(runtimeTool + "\n" + string(encoded)))
	fingerprint := fmt.Sprintf("%x", digest[:12])
	if state.toolOccurrences == nil {
		state.toolOccurrences = map[string]int{}
	}
	state.toolOccurrences[fingerprint]++
	return fmt.Sprintf("chat-%s-%s-%d", state.requestID, fingerprint, state.toolOccurrences[fingerprint])
}

const (
	dashboardCommandYieldMilliseconds   = 10000
	dashboardCommandTimeoutMilliseconds = 10 * 60 * 1000
	dashboardCommandFollowWait          = 20 * time.Second
	dashboardCommandPollInterval        = 500 * time.Millisecond
)

func dashboardCommandKey(args map[string]any) string {
	command, _ := args["command"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	workspace, _ := args["workspace"].(string)
	cwd, _ := args["cwd"].(string)
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = "."
	}
	encoded, _ := json.Marshal(map[string]string{
		"workspace": strings.TrimSpace(workspace),
		"cwd":       cwd,
		"command":   command,
	})
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:16])
}

func dashboardProcessState(value any) (processID string, running bool, ok bool) {
	snapshot, ok := value.(map[string]any)
	if !ok {
		return "", false, false
	}
	processID, _ = snapshot["processId"].(string)
	processID = strings.TrimSpace(processID)
	running, hasRunning := snapshot["running"].(bool)
	return processID, running, processID != "" && hasRunning
}

func dashboardToolProcessState(result string) (processID string, running bool, ok bool) {
	var payload struct {
		Result map[string]any `json:"result"`
	}
	if json.Unmarshal([]byte(result), &payload) != nil {
		return "", false, false
	}
	return dashboardProcessState(payload.Result)
}

func dashboardRunningProcessProgress(result string) string {
	var payload struct {
		Result map[string]any `json:"result"`
	}
	if json.Unmarshal([]byte(result), &payload) != nil {
		return ""
	}
	processID, running, ok := dashboardProcessState(payload.Result)
	if !ok || !running {
		return ""
	}
	progress := map[string]any{"processId": processID, "running": true, "status": payload.Result["status"]}
	if stdout, exists := payload.Result["stdout"]; exists {
		progress["stdout"] = stdout
	}
	if stderr, exists := payload.Result["stderr"]; exists {
		progress["stderr"] = stderr
	}
	encoded, _ := json.Marshal(progress)
	return string(encoded)
}

func dashboardSeedActiveCommands(state *dashboardExecutionState, results []dashboardToolCall) {
	if state == nil {
		return
	}
	if state.activeCommands == nil {
		state.activeCommands = map[string]string{}
	}
	for _, result := range results {
		processID, running, ok := dashboardToolProcessState(result.Result)
		if !ok {
			continue
		}
		if result.Name == "poll_project_command" {
			if !running {
				for key, activeID := range state.activeCommands {
					if activeID == processID {
						delete(state.activeCommands, key)
					}
				}
			}
			continue
		}
		if result.Name != "run_project_command" {
			continue
		}
		args := map[string]any{}
		if json.Unmarshal([]byte(result.Arguments), &args) != nil {
			continue
		}
		key := dashboardCommandKey(args)
		if key == "" {
			continue
		}
		if running {
			state.activeCommands[key] = processID
		} else {
			delete(state.activeCommands, key)
		}
	}
}

type dashboardProcessPoller func(context.Context, string) (gateway.RoutedResult, error)

func dashboardRouteProjectCommand(state *dashboardExecutionState, original, forward map[string]any) (runtimeTool string, sideEffect bool, routed map[string]any, commandKey string, reused bool) {
	commandKey = dashboardCommandKey(original)
	if _, exists := forward["yieldMs"]; !exists {
		forward["yieldMs"] = dashboardCommandYieldMilliseconds
	}
	if _, exists := forward["timeoutMs"]; !exists {
		forward["timeoutMs"] = dashboardCommandTimeoutMilliseconds
	}
	if state != nil && commandKey != "" {
		if processID := strings.TrimSpace(state.activeCommands[commandKey]); processID != "" {
			return "process_poll", false, map[string]any{"processId": processID, "cursor": 0}, commandKey, true
		}
	}
	return "run_command", true, forward, commandKey, false
}

func dashboardFollowRunningProcess(ctx context.Context, initial gateway.RoutedResult, maxWait, interval time.Duration, poll dashboardProcessPoller) (gateway.RoutedResult, error) {
	current := initial
	processID, running, ok := dashboardProcessState(current.Result)
	if !current.OK || !ok || !running || poll == nil || maxWait <= 0 {
		return current, nil
	}
	if interval <= 0 {
		interval = time.Millisecond
	}
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-timer.C:
			return current, nil
		case <-ticker.C:
			next, err := poll(ctx, processID)
			if err != nil {
				return current, err
			}
			current = next
			_, running, ok = dashboardProcessState(current.Result)
			if !current.OK || !ok || !running {
				return current, nil
			}
		}
	}
}

func execDashboardRuntimeTool(r *http.Request, s *Server, userID, name string, args map[string]any) (string, bool) {
	if name == "list_plugin_tools" {
		return s.execDashboardPluginListTools(r, userID, args)
	}
	if name == "call_plugin_tool" {
		return s.execDashboardPluginCallTool(r, userID, args)
	}
	runtimeTool, sideEffect, forward, ok := dashboardRuntimeToolSpec(name, args)
	if !ok {
		return "", false
	}
	if s.Hub == nil {
		return `{"error":"runtime_gateway_unavailable"}`, true
	}
	workspace, err := dashboardResolveRuntimeWorkspace(r, s, userID, args)
	if err != nil {
		b, _ := json.Marshal(map[string]any{"error": "workspace_unavailable", "message": err.Error()})
		return string(b), true
	}
	state := dashboardExecutionStateFromRequest(r)
	commandKey := ""
	reusedProcess := false
	if name == "run_project_command" {
		runtimeTool, sideEffect, forward, commandKey, reusedProcess = dashboardRouteProjectCommand(state, args, forward)
	}
	sessionID := "dashboard-" + userID
	if state != nil && state.sessionID != "" {
		sessionID = state.sessionID
	}
	requestID := dashboardRuntimeRequestID(state, runtimeTool, forward)
	result, err := s.Hub.Call(r.Context(), userID, workspace.Key, sessionID, runtimeTool, forward, sideEffect, requestID)
	if err != nil && !sideEffect && s.Workspaces != nil {
		// Read-only runtime operations are safe to replay after a route loss. This
		// mirrors the MCP gateway rebind behavior while keeping mutations strictly
		// single-shot unless the runtime itself provides an approval/retry flow.
		if rebound, rebindErr := s.Workspaces.Activate(r.Context(), userID, workspace.Key); rebindErr == nil && rebound != nil {
			workspace = rebound
			dashboardSetExecutionWorkspace(r, rebound)
			result, err = s.Hub.Call(r.Context(), userID, workspace.Key, sessionID, runtimeTool, forward, sideEffect, requestID)
		}
	}
	if err != nil {
		b, _ := json.Marshal(map[string]any{"error": "runtime_call_failed", "message": err.Error()})
		return string(b), true
	}
	pollErr := error(nil)
	result, pollErr = dashboardFollowRunningProcess(r.Context(), result, dashboardCommandFollowWait, dashboardCommandPollInterval, func(ctx context.Context, processID string) (gateway.RoutedResult, error) {
		pollArgs := map[string]any{"processId": processID, "cursor": 0}
		pollRequestID := dashboardRuntimeRequestID(state, "process_poll", pollArgs)
		return s.Hub.Call(ctx, userID, workspace.Key, sessionID, "process_poll", pollArgs, false, pollRequestID)
	})
	if state != nil && commandKey != "" {
		if state.activeCommands == nil {
			state.activeCommands = map[string]string{}
		}
		processID, running, hasState := dashboardProcessState(result.Result)
		switch {
		case hasState && running:
			state.activeCommands[commandKey] = processID
		case hasState || !result.OK:
			delete(state.activeCommands, commandKey)
		}
	}
	if state != nil && name == "poll_project_command" {
		processID, running, hasState := dashboardProcessState(result.Result)
		if hasState && !running {
			for key, activeID := range state.activeCommands {
				if activeID == processID {
					delete(state.activeCommands, key)
				}
			}
		}
	}
	if name == "review_project_diff" {
		// Git output can be arbitrarily large. Bound the LLM/SSE/persistence payload,
		// and explicitly tell the client when a file-scoped follow-up is required.
		if gitResult, ok := result.Result.(map[string]any); ok {
			diff, _ := gitResult["diff"].(string)
			const maxGitDiffBytes = 64 * 1024
			truncated := len(diff) > maxGitDiffBytes
			if truncated {
				end := maxGitDiffBytes
				for end > 0 && !utf8.ValidString(diff[:end]) {
					end--
				}
				diff = diff[:end]
			}
			result.Result = map[string]any{
				"diff": diff, "truncated": truncated,
				"repositoryCount":     gitResult["repositoryCount"],
				"changedRepositories": gitResult["changedRepositories"],
			}
		}
	}
	payload := map[string]any{"ok": result.OK, "workspace": workspace.WorkspaceName, "runtimeTool": runtimeTool, "result": result.Result, "metadata": result.Metadata}
	if reusedProcess {
		payload["reusedProcess"] = true
	}
	if _, running, hasState := dashboardProcessState(result.Result); hasState && running {
		payload["nextAction"] = "Call poll_project_command with this processId; do not call run_project_command again for the same command."
	}
	if pollErr != nil {
		payload["pollError"] = pollErr.Error()
	}
	if !result.OK {
		payload["error"] = result.ErrorCode
		payload["message"] = result.Error
	}
	b, _ := json.Marshal(payload)
	return string(b), true
}
