package mcpgateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/0xmarkhydra/codelocal/internal/orchestration"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const compactOrchestrationInstructions = `CodeLocal connects MCP-compatible AI clients and coding agents to explicitly authorized local workspaces through one shared Project Brain and controlled runtime. Reuse prior results. Pass the exact workspaceKey on workspace-scoped calls; legacy stateful clients may reuse a selected workspace, while modern sessionless MCP clients must carry workspaceKey explicitly between calls. Minimize MCP round trips: when the user asks for a continuation, a workflow with two or more dependent steps, or a cross-domain sequence such as browser/computer plus edit/verify/git, prefer one bounded agent call that performs the full safe sequence and checkpoints progress after each step. This reduces loss of work when an AI host reconnects or drops a connector between turns. For coding/debugging/review/refactor, call context(action=task) early; use context's project/dependency/LSP actions for exact relationships, read only for targeted expansion, and search mainly for literal/config/log text. Use edit for mutations, then verify and the smallest relevant terminal checks. Use terminal for both command execution and process lifecycle, git only for Git work, and mcp lazily for installed extensions. For websites, inspect with browser snapshot/find before interacting. For desktop apps, prefer computer observe and semantic targets; use raw element IDs or coordinates only as fallbacks. For repeatable multi-step browser/desktop workflows, prefer agent with the concrete user objective and semantic steps so verified runs can be learned locally and replayed as a faster path on similar future requests. When the user explicitly asks to remember something, or states a durable goal, preference, constraint, milestone or confirmed project decision that materially affects future work, workspace(action=remember) can persist a compact sanitized fact to CodeLocal memory; use a stable memory key for facts whose value may change. When prior durable user/project context may materially affect an answer, workspace(action=recall) can retrieve it with a focused query, including global memory without selecting a workspace. Do not store secrets or routine small talk. Browser and Computer Use are separate opt-in domains, and first-run consent never replaces action-level approval. Avoid repeated inspection unless state changed. Local security policy and explicit user approval in the current MCP client remain authoritative for side effects.`

type compactToolDef struct {
	Name        string
	Title       string
	Description string
	Schema      json.RawMessage
	Meta        mcp.Meta
	Annotations *mcp.ToolAnnotations
	Resolve     func(map[string]any) (operationInvocation, map[string]any, error)
	Execute     func(context.Context, *Service, string, map[string]any, *mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

func compactAnnotations(title string, readOnly, destructive, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    readOnly,
		DestructiveHint: boolPtr(destructive),
		OpenWorldHint:   boolPtr(openWorld),
	}
}

func actionSchema(actions []string, properties map[string]any) json.RawMessage {
	if properties == nil {
		properties = map[string]any{}
	}
	properties["action"] = map[string]any{"type": "string", "enum": actions, "description": "Operation to perform."}
	if _, ok := properties["workspaceKey"]; !ok {
		properties["workspaceKey"] = workspaceKeySchema
	}
	return objectSchema(properties, "action")
}

func schemaProperty(schema json.RawMessage, name string) any {
	var decoded map[string]any
	if json.Unmarshal(schema, &decoded) != nil {
		return nil
	}
	properties, _ := decoded["properties"].(map[string]any)
	if properties == nil {
		return nil
	}
	return properties[name]
}

func cloneArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for key, value := range args {
		out[key] = value
	}
	return out
}

func requireArgs(args map[string]any, action string, names ...string) error {
	for _, name := range names {
		if _, ok := args[name]; !ok {
			return fmt.Errorf("%s requires %s", action, name)
		}
	}
	return nil
}

func resolveAction(args map[string]any, actions map[string]string, required map[string][]string) (operationInvocation, map[string]any, error) {
	raw, _ := args["action"].(string)
	resolved := orchestration.ResolveAction(raw)
	available := make([]string, 0, len(actions))
	for action := range actions {
		available = append(available, action)
	}
	if err := orchestration.NewActionValidator(available).Validate(resolved); err != nil {
		if orchestration.IsReplanRequired(err) {
			return operationInvocation{}, nil, err
		}
		return operationInvocation{}, nil, unsupportedActionSchemaError(resolved.Normalized)
	}
	action := resolved.Normalized
	runtimeTool := actions[action]
	forward := cloneArgs(args)
	delete(forward, "action")
	if err := requireArgs(forward, action, required[action]...); err != nil {
		return operationInvocation{}, nil, err
	}
	operation, err := operationForRuntimeTool(runtimeTool)
	if err != nil {
		return operationInvocation{}, nil, err
	}
	return operation, forward, nil
}

func singleOperationResolver(runtimeTool string, required ...string) func(map[string]any) (operationInvocation, map[string]any, error) {
	return func(args map[string]any) (operationInvocation, map[string]any, error) {
		forward := cloneArgs(args)
		if err := requireArgs(forward, runtimeTool, required...); err != nil {
			return operationInvocation{}, nil, err
		}
		operation, err := operationForRuntimeTool(runtimeTool)
		return operation, forward, err
	}
}

func generationFourCompactToolDefinitions() []compactToolDef {
	path := str("Workspace-relative path.")
	approval := str("One-time approval token returned by an approval-required result.")
	processID := str("CodeLocal process ID.")
	limit := integer("Maximum results.", 1, 2000)
	agentStep := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tool": map[string]any{"type": "string", "enum": []string{"project", "context", "read", "search", "dependency", "lsp", "edit", "verify", "git", "terminal", "browser", "computer"}, "description": "Compact CodeLocal tool to execute inside this bounded run."},
			"args": anyObject("Arguments for the compact tool, including its action discriminator when required."),
		},
		"required":             []string{"tool"},
		"additionalProperties": false,
	}
	memoryItem := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":       map[string]any{"type": "string", "enum": []string{"goal", "preference", "decision", "user_fact", "project_fact", "idea", "constraint", "milestone", "problem", "person", "company"}, "description": "Durable fact category."},
			"key":        map[string]any{"type": "string", "minLength": 1, "maxLength": 160, "description": "Optional stable fact key. Reuse the same key when a mutable fact changes so the latest value replaces the older one, for example user.goal.codelocal_users."},
			"summary":    map[string]any{"type": "string", "minLength": 1, "maxLength": 1200, "description": "Compact fact to remember. Never include secrets."},
			"scope":      map[string]any{"type": "string", "enum": []string{"global", "project", "workspace"}, "description": "Global user memory, logical-project memory shared across its checkouts, or current-workspace memory."},
			"importance": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Optional importance score."},
			"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Optional confidence score."},
		},
		"required":             []string{"kind", "summary", "scope"},
		"additionalProperties": false,
	}

	deviceActions := map[string]string{"active": "list_devices", "paired": "list_device_identities", "rename": "rename_device", "revoke": "revoke_device"}
	workspaceActions := map[string]string{"list": "list_workspaces", "select": "select_workspace", "info": "workspace_info", "access": "approval_mode", "remember": "memory_remember", "recall": "memory_recall", "skills": "learned_skill_list"}
	projectActions := map[string]string{"info": "project_info", "map": "project_map", "instructions": "read_instructions"}
	readActions := map[string]string{"info": "file_info", "file": "read_file", "range": "read_file_range", "many": "read_files"}
	searchActions := map[string]string{"files": "list_files", "text": "search_code"}
	dependencyActions := map[string]string{"inspect": "inspect_dependency", "read": "read_dependency", "search": "search_dependency"}
	lspActions := map[string]string{
		"info": "semantic_info", "workspace_symbols": "workspace_symbols", "document_symbols": "document_symbols",
		"definition": "find_definition", "references": "find_references", "implementations": "find_implementations",
		"hover": "get_hover", "diagnostics": "get_diagnostics", "callers": "get_callers", "callees": "get_callees", "import_graph": "get_import_graph",
	}
	editActions := map[string]string{"write": "write_file", "replace": "edit_file", "patch": "apply_patch", "apply": "apply_edits", "format": "format_changed_files"}
	verifyActions := map[string]string{"snapshot": "snapshot_diagnostics", "changes": "verify_changes"}
	gitActions := map[string]string{
		"status": "git_status", "diff": "git_diff", "log": "git_log", "show": "git_show", "blame": "git_blame",
		"file_history": "git_file_history", "stage": "git_stage", "unstage": "git_unstage", "commit": "git_commit", "push": "git_push",
	}
	terminalActions := map[string]string{"preflight": "terminal_preflight", "history": "terminal_history", "run": "run_command", "start": "exec_start", "start_pty": "pty_start"}
	processActions := map[string]string{"list": "process_list", "poll": "exec_poll", "write": "exec_write", "resize": "pty_resize", "signal": "exec_signal", "kill": "exec_kill", "cancel": "exec_cancel"}
	approvalActions := map[string]string{"list": "approval_list", "revoke": "approval_revoke", "reset": "approval_reset"}
	securityActions := map[string]string{"info": "sandbox_info", "smoke_test": "sandbox_smoke_test"}
	mcpActions := map[string]string{"list": "mcp_list", "search": "mcp_search_tools", "info": "mcp_tool_info", "call": "mcp_call"}

	tools := []compactToolDef{
		{
			Name: "device", Title: "Manage CodeLocal devices", Description: "List active/paired CodeLocal devices or rename/revoke a paired device. Use only for device/account management.",
			Schema:      actionSchema([]string{"active", "paired", "rename", "revoke"}, map[string]any{"credentialId": str("Paired device credential ID."), "deviceName": str("New device name.")}),
			Annotations: compactAnnotations("Manage CodeLocal devices", false, true, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, deviceActions, map[string][]string{"rename": {"credentialId", "deviceName"}, "revoke": {"credentialId"}})
			},
		},
		{
			Name: "workspace", Title: "Manage workspaces, access, memory and learned skills", Description: "List/select/inspect a CodeLocal workspace, manage its chat-first access mode, remember/recall durable memory, or inspect learned skills. For action=access, omit mode to read the current choice or set mode to prompt, smart, or full only after the user chooses: Yêu cầu phê duyệt, Phê duyệt giúp tôi, or Toàn quyền truy cập. Access mode is stored locally by workspace and survives MCP/session reconnects.",
			Schema: actionSchema([]string{"list", "select", "info", "access", "remember", "recall", "skills"}, map[string]any{
				"key":      str("Workspace key returned by action=list."),
				"mode":     map[string]any{"type": "string", "enum": []string{"prompt", "smart", "full"}, "description": "Access mode for action=access: prompt = Yêu cầu phê duyệt; smart = Phê duyệt giúp tôi; full = Toàn quyền truy cập. Omit to inspect current mode."},
				"query":    str("Focused natural-language memory query for action=recall."),
				"limit":    integer("Maximum recalled memories or learned skills.", 1, 20),
				"memories": map[string]any{"type": "array", "minItems": 1, "maxItems": 12, "items": memoryItem, "description": "Durable sanitized facts to persist. Project facts follow the logical project across machines; global facts do not require a workspace; workspace facts stay checkout-local. Use a stable key for mutable facts so later updates replace older values."},
			}),
			Annotations: compactAnnotations("Manage workspaces, access and memory", false, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, workspaceActions, map[string][]string{"select": {"key"}, "remember": {"memories"}, "recall": {"query"}})
			},
		},
		{
			Name: "project", Title: "Inspect project", Description: "Read stable project metadata, compact project structure, or scoped AGENTS.md instructions.",
			Schema:      actionSchema([]string{"info", "map", "instructions"}, map[string]any{"force": boolean("Force project-map refresh."), "path": path}),
			Annotations: compactAnnotations("Inspect project", true, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, projectActions, nil)
			},
		},
		{
			Name: "context", Title: "Find task context", Description: "Primary semantic-first retrieval and planning step for coding/debug/review/refactor. Returns ranked symbols, graph neighbors, bounded snippets, durable task memory, a capability-aware agent plan, verification strategy and next action; call before broad scans and refresh after recovery when evidence becomes stale.",
			Schema:      objectSchema(map[string]any{"taskHint": str("Concrete coding task."), "targets": array(path, "Optional concrete workspace-relative files/directories discovered during work. Explicit targets force Project Brain to re-resolve repository/directory rules for the actual working set."), "limit": integer("Maximum ranked results.", 1, 100), "workspaceKey": workspaceKeySchema}, "taskHint"),
			Annotations: compactAnnotations("Find task context", true, false, false), Resolve: singleOperationResolver("context_for_task", "taskHint"),
		},
		{
			Name: "agent", Title: "Run bounded agent plan", Description: "Execute a bounded multi-step CodeLocal action program in one MCP call. Prefer this for continuation-sensitive work and workflows with multiple dependent steps (for example browser/computer -> edit -> verify -> git) so progress is checkpointed locally while minimizing AI-host MCP round trips. CodeLocal automatically grounds the objective with context, validates every step against a conservative autonomous policy, replans from fresh task state, and can run context-aware verification after edits. It never auto-confirms approvals, Git writes/pushes, open-world actions, physical input, or other unbounded side effects.",
			Schema: objectSchema(map[string]any{
				"objective":     str("Concrete objective for this bounded execution run."),
				"steps":         map[string]any{"type": "array", "minItems": 1, "maxItems": 12, "items": agentStep, "description": "Model-authored action program. Later steps must not depend on unseen output from earlier steps."},
				"autoVerify":    boolean("After successful edits, automatically run verify.changes and missing recognized verification checks. Defaults to true."),
				"stopWhenReady": boolean("Stop once the quality gate reaches ready. Defaults to true."),
				"responseMode":  map[string]any{"type": "string", "enum": []string{"compact", "full"}, "description": "Response verbosity. Defaults to compact; use full for debugging/review to include the full plan and execution trace."},
				"workspaceKey":  workspaceKeySchema,
			}, "objective", "steps"),
			Annotations: compactAnnotations("Run bounded agent plan", false, true, true),
			Execute: func(ctx context.Context, service *Service, userID string, args map[string]any, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return service.runBoundedAgent(ctx, userID, args, req)
			},
		},
		{
			Name: "read", Title: "Read project files", Description: "Read file metadata, one file, a line range, or a targeted batch. Prefer paths selected by context/LSP rather than broad source dumping.",
			Schema:      actionSchema([]string{"info", "file", "range", "many"}, map[string]any{"path": path, "paths": array(path, "Target files."), "startLine": integer("1-based first line.", 1, 0), "endLine": integer("1-based last line.", 1, 0)}),
			Annotations: compactAnnotations("Read project files", true, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, readActions, map[string][]string{"info": {"path"}, "file": {"path"}, "range": {"path", "startLine", "endLine"}, "many": {"paths"}})
			},
		},
		{
			Name: "search", Title: "Search project", Description: "List project files or search literal/unknown text. Prefer context/LSP for semantic code discovery.",
			Schema:      actionSchema([]string{"files", "text"}, map[string]any{"query": str("Text query."), "path": path, "maxDepth": integer("Maximum directory depth.", 0, 20), "maxResults": integer("Maximum text matches.", 1, 1000), "fixedStrings": boolean("Treat query literally."), "includeIgnored": boolean("Include gitignored paths.")}),
			Annotations: compactAnnotations("Search project", true, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, searchActions, map[string][]string{"text": {"query"}})
			},
		},
		{
			Name: "dependency", Title: "Inspect dependencies", Description: "Inspect dependency metadata or read/search an installed dependency without broad repository scanning.",
			Schema:      actionSchema([]string{"inspect", "read", "search"}, map[string]any{"name": str("Dependency/package/module name."), "ecosystem": str("node, python, rust, go, or auto."), "path": str("Path inside dependency."), "query": str("Search query."), "startLine": integer("First line.", 1, 0), "endLine": integer("Last line.", 1, 0), "maxResults": integer("Maximum matches.", 1, 500), "fixedStrings": boolean("Literal search.")}),
			Annotations: compactAnnotations("Inspect dependencies", true, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, dependencyActions, map[string][]string{"inspect": {"name"}, "read": {"name"}, "search": {"name", "query"}})
			},
		},
		{
			Name: "lsp", Title: "Navigate code intelligence", Description: "Use semantic/LSP code intelligence for symbols, definitions, references, implementations, hover, diagnostics, call graph and import graph.",
			Schema:      actionSchema([]string{"info", "workspace_symbols", "document_symbols", "definition", "references", "implementations", "hover", "diagnostics", "callers", "callees", "import_graph"}, map[string]any{"path": path, "line": integer("1-based line.", 1, 0), "column": integer("1-based column.", 1, 0), "name": str("Symbol name."), "query": str("Symbol/fallback query."), "limit": limit}),
			Annotations: compactAnnotations("Navigate code intelligence", true, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, lspActions, map[string][]string{"document_symbols": {"path"}, "implementations": {"path", "line", "column"}, "hover": {"path", "line", "column"}})
			},
		},
		{
			Name: "edit", Title: "Edit project files", Description: "Create/write, exact-replace, patch, transactionally edit, or format project files. Prefer hash-safe structured edits for multi-file changes.",
			Schema:      actionSchema([]string{"write", "replace", "patch", "apply", "format"}, map[string]any{"path": path, "content": str("Complete file content."), "oldText": str("Exact text to replace."), "newText": str("Replacement text."), "replaceAll": boolean("Replace all occurrences."), "expectedHash": str("Optional SHA-256 from previous read."), "patch": str("Unified Git patch."), "files": array(anyObject("Structured file edit."), "File edit transactions."), "paths": array(path, "Changed files to format.")}),
			Annotations: compactAnnotations("Edit project files", false, true, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, editActions, map[string][]string{"write": {"path", "content"}, "replace": {"path", "oldText", "newText"}, "patch": {"patch"}, "apply": {"files"}, "format": {"paths"}})
			},
		},
		{
			Name: "verify", Title: "Verify code changes", Description: "Create a diagnostic baseline or verify edits with diagnostics regression, Git diff and recommended checks.",
			Schema:      actionSchema([]string{"snapshot", "changes"}, map[string]any{"paths": array(path, "Paths to inspect."), "baselineId": str("Optional baseline ID.")}),
			Annotations: compactAnnotations("Verify code changes", true, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, verifyActions, nil)
			},
		},
		{
			Name: "git", Title: "Use Git", Description: "Read Git state/history or perform reviewed stage/unstage/commit/push operations. Force push remains blocked by local policy.",
			Schema:      actionSchema([]string{"status", "diff", "log", "show", "blame", "file_history", "stage", "unstage", "commit", "push"}, map[string]any{"repository": str("Optional repository ID or workspace-relative repository root for multi-repo workspaces."), "cached": boolean("Read staged diff."), "path": path, "paths": array(path, "Git paths."), "limit": integer("Commit count.", 1, 100), "ref": str("Git ref."), "startLine": integer("First line.", 1, 0), "endLine": integer("Last line.", 1, 0), "message": str("Commit message."), "expectedPaths": array(path, "Expected staged paths."), "remote": str("Remote name."), "branch": str("Branch/ref."), "force": boolean("Must remain false."), "approvalToken": approval}),
			Annotations: compactAnnotations("Use Git", false, false, true),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, gitActions, map[string][]string{"blame": {"path"}, "file_history": {"path"}, "stage": {"paths"}, "unstage": {"paths"}, "commit": {"message"}})
			},
		},
		{
			Name: "terminal", Title: "Use terminal", Description: "Preflight a command, inspect terminal history, or run/start guarded host processes. Local policy and explicit user approval in the current MCP client remain authoritative.",
			Schema:      actionSchema([]string{"preflight", "history", "run", "start", "start_pty"}, map[string]any{"command": str("Shell command."), "cwd": path, "approvalToken": approval, "yieldMs": integer("Initial wait milliseconds.", 0, 10000), "timeoutMs": integer("Timeout milliseconds; 0 disables timeout.", 0, 3600000), "query": str("Terminal-history query."), "event": map[string]any{"type": "string", "enum": []string{"started", "finished", "all"}}, "limit": integer("History record limit.", 1, 500)}),
			Annotations: compactAnnotations("Use terminal", false, true, true),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, terminalActions, map[string][]string{"preflight": {"command"}, "run": {"command"}, "start": {"command"}, "start_pty": {"command"}})
			},
		},
		{
			Name: "process", Title: "Control running processes", Description: "List, poll, write to, resize, signal, kill or cancel CodeLocal-started processes. Poll/write/signal work for both normal and PTY processes.",
			Schema:      actionSchema([]string{"list", "poll", "write", "resize", "signal", "kill", "cancel"}, map[string]any{"processId": processID, "stdoutCursor": integer("Stdout byte cursor.", 0, 0), "stderrCursor": integer("Stderr byte cursor.", 0, 0), "input": str("UTF-8 process input."), "cols": integer("PTY columns.", 10, 500), "rows": integer("PTY rows.", 5, 300), "signal": map[string]any{"type": "string", "enum": []string{"SIGTERM", "SIGINT", "SIGKILL"}}, "reason": str("Cancellation reason.")}),
			Annotations: compactAnnotations("Control running processes", false, true, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, processActions, map[string][]string{"poll": {"processId"}, "write": {"processId", "input"}, "resize": {"processId", "cols", "rows"}, "signal": {"processId"}, "kill": {"processId"}, "cancel": {"processId"}})
			},
		},
		{
			Name: "approvals", Title: "Manage remembered approvals", Description: "List, revoke, or reset locally remembered workspace-scoped approvals. This never grants new permissions.",
			Schema:      actionSchema([]string{"list", "revoke", "reset"}, map[string]any{"id": str("Approval ID."), "actionKey": str("Structured approval action key.")}),
			Annotations: compactAnnotations("Manage remembered approvals", false, true, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				operation, forward, err := resolveAction(args, approvalActions, nil)
				id, _ := forward["id"].(string)
				actionKey, _ := forward["actionKey"].(string)
				if err == nil && strings.TrimSpace(actionKey) == "" && strings.TrimSpace(id) == "" && operation.OperationID == "approvals.revoke" {
					return operationInvocation{}, nil, fmt.Errorf("revoke requires id or actionKey")
				}
				return operation, forward, err
			},
		},
		{
			Name: "security", Title: "Inspect execution security", Description: "Inspect the active host-policy execution model or run its compatibility smoke test.",
			Schema: actionSchema([]string{"info", "smoke_test"}, nil), Annotations: compactAnnotations("Inspect execution security", true, false, false),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, securityActions, nil)
			},
		},
		{
			Name: "mcp", Title: "Use installed MCP extensions", Description: "Lazily list/search/inspect/call installed local MCP extensions without exposing every extension tool globally. Calls remain approval-controlled.",
			Schema:      actionSchema([]string{"list", "search", "info", "call"}, map[string]any{"query": str("Tool search query."), "limit": integer("Maximum search results.", 1, 50), "server": str("MCP server name."), "refresh": boolean("Refresh extension catalog."), "tool": str("Extension tool name."), "arguments": anyObject("Extension tool arguments."), "approvalToken": approval}),
			Annotations: compactAnnotations("Use installed MCP extensions", false, true, true),
			Resolve: func(args map[string]any) (operationInvocation, map[string]any, error) {
				return resolveAction(args, mcpActions, map[string][]string{"info": {"server", "tool"}, "call": {"server", "tool"}})
			},
		},
	}
	tools = append(tools, compactSocialToolDefinitions()...)
	tools = append(tools, compactBlogToolDefinitions()...)
	return append(tools, generationFiveCompactAutomationToolDefinitions()...)
}

func compactToolDefinitions() []compactToolDef {
	generationFour := generationFourCompactToolDefinitions()
	byName := make(map[string]compactToolDef, len(generationFour))
	for _, def := range generationFour {
		byName[def.Name] = def
	}
	for _, def := range compactAutomationToolDefinitions() {
		byName[def.Name] = def
	}

	path := str("Workspace-relative path.")
	approval := str("One-time approval token returned by an approval-required result.")
	processID := str("CodeLocal process ID.")
	limit := integer("Maximum results.", 1, 2000)

	workspaceActions := map[string]string{
		"list": "list_workspaces", "select": "select_workspace", "info": "workspace_info", "access": "approval_mode", "execution": "execution_mode",
		"remember": "memory_remember", "recall": "memory_recall", "skills": "learned_skill_list",
		"devices": "list_devices", "paired_devices": "list_device_identities", "rename_device": "rename_device", "revoke_device": "revoke_device",
		"approvals": "approval_list", "revoke_approval": "approval_revoke", "reset_approvals": "approval_reset",
		"security": "sandbox_info", "security_smoke_test": "sandbox_smoke_test",
	}
	workspace := byName["workspace"]
	workspace.Title = "Manage CodeLocal workspace and runtime"
	workspace.Description = "Manage workspaces, access, Safe Workspace / Live Project execution choice, durable memory, learned skills, paired devices, remembered approvals, and execution-security diagnostics through one runtime-scoped tool. Device revocation and approval changes remain policy-controlled."
	workspace.Schema = actionSchema(
		[]string{"list", "select", "info", "access", "execution", "remember", "recall", "skills", "devices", "paired_devices", "rename_device", "revoke_device", "approvals", "revoke_approval", "reset_approvals", "security", "security_smoke_test"},
		map[string]any{
			"key":           str("Workspace key returned by action=list."),
			"makeDefault":   boolean("For action=select, persist this workspace as the user's default across future MCP sessions. Set true only when the user explicitly asks to remember the choice."),
			"mode":          map[string]any{"type": "string", "enum": []string{"prompt", "smart", "full"}, "description": "Access mode for action=access."},
			"executionMode": map[string]any{"type": "string", "enum": []string{"safe", "live"}, "description": "For action=execution: safe = Safe Workspace (isolated, recommended/default); live = Live Project (edit the active checkout). Omit to inspect the current choice."},
			"query":         str("Focused natural-language memory query for action=recall."),
			"limit":         integer("Maximum recalled memories or learned skills.", 1, 20),
			"memories":      schemaProperty(byName["workspace"].Schema, "memories"),
			"credentialId":  str("Paired device credential ID."),
			"deviceName":    str("New device name."),
			"id":            str("Remembered approval ID."),
			"actionKey":     str("Structured approval action key."),
		},
	)
	workspace.Annotations = compactAnnotations("Manage CodeLocal workspace and runtime", false, true, false)
	workspace.Resolve = func(args map[string]any) (operationInvocation, map[string]any, error) {
		operation, forward, err := resolveAction(args, workspaceActions, map[string][]string{
			"select": {"key"}, "remember": {"memories"}, "recall": {"query"},
			"rename_device": {"credentialId", "deviceName"}, "revoke_device": {"credentialId"},
		})
		if err != nil {
			return operationInvocation{}, nil, err
		}
		if operation.OperationID == "approvals.revoke" {
			id, _ := forward["id"].(string)
			actionKey, _ := forward["actionKey"].(string)
			if strings.TrimSpace(id) == "" && strings.TrimSpace(actionKey) == "" {
				return operationInvocation{}, nil, fmt.Errorf("revoke_approval requires id or actionKey")
			}
		}
		return operation, forward, nil
	}

	contextActions := map[string]string{
		"task": "context_for_task", "project_info": "project_info", "project_map": "project_map", "instructions": "read_instructions",
		"dependency_inspect": "inspect_dependency", "dependency_read": "read_dependency", "dependency_search": "search_dependency",
		"lsp_info": "semantic_info", "workspace_symbols": "workspace_symbols", "document_symbols": "document_symbols",
		"definition": "find_definition", "references": "find_references", "implementations": "find_implementations",
		"hover": "get_hover", "diagnostics": "get_diagnostics", "callers": "get_callers", "callees": "get_callees", "import_graph": "get_import_graph",
	}
	contextDef := byName["context"]
	contextDef.Title = "Inspect project context"
	contextDef.Description = "Primary semantic-first inspection tool. Use task for ranked Project Brain context; project_* for metadata/instructions; dependency_* for installed packages; and LSP actions for exact symbols, definitions, references, diagnostics and call/import graphs."
	contextDef.Schema = actionSchema(
		[]string{"task", "project_info", "project_map", "instructions", "dependency_inspect", "dependency_read", "dependency_search", "lsp_info", "workspace_symbols", "document_symbols", "definition", "references", "implementations", "hover", "diagnostics", "callers", "callees", "import_graph"},
		map[string]any{
			"taskHint":     str("Concrete coding/debug/review/refactor task."),
			"targets":      array(path, "Optional workspace-relative targets for task context."),
			"force":        boolean("Force project-map refresh."),
			"path":         path,
			"name":         str("Dependency or symbol name."),
			"ecosystem":    str("Dependency ecosystem: node, python, rust, go, or auto."),
			"query":        str("Dependency/symbol/fallback query."),
			"startLine":    integer("First line.", 1, 0),
			"endLine":      integer("Last line.", 1, 0),
			"maxResults":   integer("Maximum dependency search matches.", 1, 500),
			"fixedStrings": boolean("Treat dependency search literally."),
			"line":         integer("1-based line.", 1, 0),
			"column":       integer("1-based column.", 1, 0),
			"limit":        limit,
		},
	)
	contextDef.Resolve = func(args map[string]any) (operationInvocation, map[string]any, error) {
		// Generation four exposed context without an action discriminator. Keep
		// direct compatibility for callers that still send only taskHint.
		if _, hasAction := args["action"]; !hasAction {
			if taskHint, _ := args["taskHint"].(string); strings.TrimSpace(taskHint) != "" {
				args = cloneArgs(args)
				args["action"] = "task"
			}
		}
		return resolveAction(args, contextActions, map[string][]string{
			"task": {"taskHint"}, "dependency_inspect": {"name"}, "dependency_read": {"name"}, "dependency_search": {"name", "query"},
			"document_symbols": {"path"}, "implementations": {"path", "line", "column"}, "hover": {"path", "line", "column"},
		})
	}

	terminalActions := map[string]string{
		"preflight": "terminal_preflight", "history": "terminal_history", "run": "run_command", "start": "exec_start", "start_pty": "pty_start", "publish_artifact": "artifact_publish",
		"process_list": "process_list", "poll": "exec_poll", "write": "exec_write", "resize": "pty_resize", "signal": "exec_signal", "kill": "exec_kill", "cancel": "exec_cancel",
	}
	terminal := byName["terminal"]
	terminal.Title = "Use terminal and processes"
	terminal.Description = "Run or start guarded commands, manage CodeLocal-started processes, and publish a finished workspace MP4 as a durable public artifact. Use publish_artifact only after the render is complete."
	terminal.Schema = actionSchema(
		[]string{"preflight", "history", "run", "start", "start_pty", "publish_artifact", "process_list", "poll", "write", "resize", "signal", "kill", "cancel"},
		map[string]any{
			"command": str("Shell command."), "cwd": path, "path": path, "approvalToken": approval,
			"yieldMs": integer("Initial wait milliseconds.", 0, 10000), "timeoutMs": integer("Timeout milliseconds; 0 disables timeout.", 0, 3600000),
			"query": str("Terminal-history query."), "event": map[string]any{"type": "string", "enum": []string{"started", "finished", "all"}}, "limit": integer("History record limit.", 1, 500),
			"processId": processID, "stdoutCursor": integer("Stdout byte cursor.", 0, 0), "stderrCursor": integer("Stderr byte cursor.", 0, 0),
			"input": str("UTF-8 process input."), "cols": integer("PTY columns.", 10, 500), "rows": integer("PTY rows.", 5, 300),
			"signal": map[string]any{"type": "string", "enum": []string{"SIGTERM", "SIGINT", "SIGKILL"}}, "reason": str("Cancellation reason."),
		},
	)
	terminal.Resolve = func(args map[string]any) (operationInvocation, map[string]any, error) {
		return resolveAction(args, terminalActions, map[string][]string{
			"preflight": {"command"}, "run": {"command"}, "start": {"command"}, "start_pty": {"command"}, "publish_artifact": {"path"},
			"poll": {"processId"}, "write": {"processId", "input"}, "resize": {"processId", "cols", "rows"}, "signal": {"processId"}, "kill": {"processId"}, "cancel": {"processId"},
		})
	}

	agent := byName["agent"]
	agent.Description += " status=needs_continuation means the objective is unfinished; use nextAction and fresh context to plan another bounded call. Only status=ready indicates verified completion. A halted result needs its reason handled before resuming."
	defs := []compactToolDef{
		workspace,
		contextDef,
		agent,
		byName["read"],
		byName["search"],
		byName["edit"],
		byName["verify"],
		byName["git"],
		terminal,
		byName["mcp"],
		byName["social"],
		byName["blog"],
		byName["browser"],
		byName["computer"],
	}
	for i := range defs {
		meta := mcp.Meta{}
		for key, value := range defs[i].Meta {
			meta[key] = value
		}
		meta["securitySchemes"] = []map[string]any{{"type": "oauth2", "scopes": []string{"mcp:tools"}}}
		defs[i].Meta = meta
	}
	return defs
}

func registerCompactTools(server *mcp.Server, service *Service, userID string) {
	for _, def := range compactToolDefinitions() {
		definition := def
		server.AddTool(&mcp.Tool{Meta: def.Meta, Name: def.Name, Title: def.Title, Annotations: def.Annotations, Description: def.Description, InputSchema: def.Schema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			legacyTool := staleToolSchemaFromContext(ctx)
			wrap := func(result *mcp.CallToolResult, err error) (*mcp.CallToolResult, error) {
				result = normalizeRecoverableToolResult(result)
				if legacyTool != "" {
					result = appendCompatibilityNotice(result, staleToolSchemaNotice(legacyTool))
				}
				return result, err
			}
			args, err := decodeArgs(req)
			if err != nil {
				return wrap(errorResult(err), nil)
			}
			if definition.Execute != nil {
				result, callErr := definition.Execute(ctx, service, userID, args, req)
				return wrap(result, callErr)
			}
			if definition.Resolve == nil {
				return wrap(errorResult(fmt.Errorf("compact tool %s has no resolver", definition.Name)), nil)
			}
			operation, forward, err := definition.Resolve(args)
			if err != nil {
				if orchestration.IsReplanRequired(err) {
					return wrap(textResult(map[string]any{
						"error":            err.Error(),
						"code":             "CODELOCAL_REPLAN_REQUIRED",
						"tool":             definition.Name,
						"status":           "replan_required",
						"requestReplan":    true,
						"executionStarted": false,
					}, true), nil)
				}
				var schemaErr *toolSchemaMismatchError
				if errors.As(err, &schemaErr) {
					return wrap(textResult(map[string]any{
						"error":       err.Error(),
						"code":        "CODELOCAL_TOOL_SCHEMA_MISMATCH",
						"tool":        definition.Name,
						"toolSurface": PublicToolSurface(),
						"action":      "reconnect_codelocal_mcp",
					}, true), nil)
				}
				return wrap(errorResult(err), nil)
			}
			result, callErr := service.callOperationRemembering(ctx, userID, definition.Name, operation, forward, req)
			service.captureDirectLearnedSkill(ctx, userID, definition.Name, operation, forward, result, callErr, req)
			return wrap(result, callErr)
		})
	}
}
