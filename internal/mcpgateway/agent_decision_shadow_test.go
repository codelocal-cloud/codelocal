package mcpgateway

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decision/providers/heuristic"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
	"github.com/0xmarkhydra/codelocal/internal/orchestration"
	"github.com/0xmarkhydra/codelocal/internal/runtimeevents"
)

func TestObserveRouteDecisionShadowDoesNotMutateBaseline(t *testing.T) {
	engine := decision.New(nil, heuristic.New(), decision.Config{})
	events := runtimeevents.NewStore(filepath.Join(t.TempDir(), "events"))
	route := orchestration.Decision{
		Primary: orchestration.LaneCode,
		Scores: map[orchestration.Lane]int{
			orchestration.LaneCode:  100,
			orchestration.LaneShell: 20,
		},
	}
	summary := observeRouteDecisionWithStore(
		context.Background(),
		engine,
		decisionruntime.ModeShadow,
		events,
		"session",
		"workspace",
		"task",
		"fix auth bug",
		route,
	)
	if summary == nil || summary["baseline"] != "code" || summary["candidate"] != "code" || summary["agreement"] != true {
		t.Fatalf("unexpected shadow summary: %#v", summary)
	}
	if route.Primary != orchestration.LaneCode {
		t.Fatalf("shadow decision mutated baseline route: %#v", route)
	}
	stored, err := events.List("workspace", "task", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Type != decisionShadowRouteEvent {
		t.Fatalf("expected one persisted shadow event, got %#v", stored)
	}
}

func TestObserveRouteDecisionOffDoesNothing(t *testing.T) {
	engine := decision.New(nil, heuristic.New(), decision.Config{})
	summary := observeRouteDecisionWithStore(
		context.Background(),
		engine,
		decisionruntime.ModeOff,
		nil,
		"",
		"",
		"",
		"task",
		orchestration.Decision{
			Primary: orchestration.LaneCode,
			Scores:  map[orchestration.Lane]int{orchestration.LaneCode: 10},
		},
	)
	if summary != nil {
		t.Fatalf("expected disabled shadow mode, got %#v", summary)
	}
}
