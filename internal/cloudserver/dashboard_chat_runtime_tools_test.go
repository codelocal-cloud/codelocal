package cloudserver

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/gateway"
)

func TestDashboardRouteProjectCommandWaitsInsteadOfRelaunching(t *testing.T) {
	args := map[string]any{"workspace": "codex-mcp", "command": "git fetch origin main"}
	key := dashboardCommandKey(args)
	state := &dashboardExecutionState{activeCommands: map[string]string{key: "process-1"}}

	secondCall := map[string]any{"workspace": "codex-mcp", "command": "git fetch origin main", "cwd": "."}
	tool, sideEffect, routed, gotKey, reused := dashboardRouteProjectCommand(state, secondCall, map[string]any{"command": args["command"], "cwd": "."})
	if tool != "process_poll" || sideEffect || !reused || gotKey != key {
		t.Fatalf("route = (%q,%v,%v,%q), want existing process poll", tool, sideEffect, reused, gotKey)
	}
	if routed["processId"] != "process-1" || routed["cursor"] != 0 {
		t.Fatalf("poll args = %#v", routed)
	}
}

func TestDashboardRouteProjectCommandAddsBoundedDefaults(t *testing.T) {
	args := map[string]any{"command": "go test ./..."}
	tool, sideEffect, routed, _, reused := dashboardRouteProjectCommand(&dashboardExecutionState{}, args, map[string]any{"command": args["command"]})
	if tool != "run_command" || !sideEffect || reused {
		t.Fatalf("route = (%q,%v,%v), want new guarded command", tool, sideEffect, reused)
	}
	if routed["yieldMs"] != dashboardCommandYieldMilliseconds || routed["timeoutMs"] != dashboardCommandTimeoutMilliseconds {
		t.Fatalf("command defaults = %#v", routed)
	}
}

func TestDashboardFollowRunningProcessReturnsTerminalSnapshot(t *testing.T) {
	initial := gateway.RoutedResult{OK: true, Result: map[string]any{"processId": "process-1", "running": true, "status": "running"}}
	polls := 0
	result, err := dashboardFollowRunningProcess(context.Background(), initial, 100*time.Millisecond, time.Millisecond, func(_ context.Context, processID string) (gateway.RoutedResult, error) {
		polls++
		if processID != "process-1" {
			t.Fatalf("process id = %q", processID)
		}
		if polls == 1 {
			return gateway.RoutedResult{OK: true, Result: map[string]any{"processId": processID, "running": true, "status": "running"}}, nil
		}
		return gateway.RoutedResult{OK: true, Result: map[string]any{"processId": processID, "running": false, "status": "exited", "exitCode": float64(0)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if polls != 2 {
		t.Fatalf("polls = %d, want 2", polls)
	}
	_, running, ok := dashboardProcessState(result.Result)
	if !ok || running {
		t.Fatalf("result = %#v, want terminal snapshot", result.Result)
	}
}

func TestDashboardResumeRestoresOnlyRunningCommands(t *testing.T) {
	state := &dashboardExecutionState{}
	args := `{"workspace":"codex-mcp","command":"git fetch origin main"}`
	dashboardSeedActiveCommands(state, []dashboardToolCall{
		{Name: "run_project_command", Arguments: args, Result: `{"ok":true,"result":{"processId":"process-1","running":true,"status":"running"}}`},
	})
	keyArgs := map[string]any{"workspace": "codex-mcp", "command": "git fetch origin main"}
	if state.activeCommands[dashboardCommandKey(keyArgs)] != "process-1" {
		t.Fatalf("active commands = %#v", state.activeCommands)
	}

	dashboardSeedActiveCommands(state, []dashboardToolCall{
		{Name: "poll_project_command", Arguments: `{"processId":"process-1"}`, Result: `{"ok":true,"result":{"processId":"process-1","running":false,"status":"exited","exitCode":0}}`},
	})
	if len(state.activeCommands) != 0 {
		t.Fatalf("completed command remained active: %#v", state.activeCommands)
	}
}

func TestDashboardRunningProcessProgressIgnoresVolatileMetadata(t *testing.T) {
	call := llmToolCall{Name: "run_project_command", Arguments: `{"command":"git fetch origin main"}`}
	a := dashboardToolProgressFingerprint(call, `{"ok":true,"metadata":{"runtimeDurationMs":100},"result":{"processId":"process-1","running":true,"status":"running","lastActivityAt":1,"stdout":{"text":""}}}`)
	b := dashboardToolProgressFingerprint(call, `{"ok":true,"metadata":{"runtimeDurationMs":900},"result":{"processId":"process-1","running":true,"status":"running","lastActivityAt":2,"stdout":{"text":""}}}`)
	if a != b {
		t.Fatalf("volatile process metadata should not count as progress: %q != %q", a, b)
	}
}

func TestDashboardRuntimeWorkspaceCannotSwitchActiveTask(t *testing.T) {
	bound := &gateway.WorkspaceView{WorkspaceName: "financial_steward", WorkspaceID: "finance-1"}
	r := dashboardWithExecutionState(httptest.NewRequest("POST", "/chat", nil), "user-1", "request-1", bound)
	s := &Server{} // No catalog required for already-bound tasks.

	for _, requested := range []string{"", "financial_steward", "finance-1"} {
		workspace, err := dashboardResolveRuntimeWorkspace(r, s, "user-1", map[string]any{"workspace": requested})
		if err != nil || workspace != bound {
			t.Fatalf("bound workspace %q = (%v,%v), want same workspace", requested, workspace, err)
		}
	}

	workspace, err := dashboardResolveRuntimeWorkspace(r, s, "user-1", map[string]any{"workspace": "unrelated_project"})
	if err == nil || workspace != nil {
		t.Fatalf("cross-project route = (%v,%v), want rejection", workspace, err)
	}
}
