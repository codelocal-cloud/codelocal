package mcpgateway

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decision/providers/heuristic"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
	"github.com/0xmarkhydra/codelocal/internal/runtimeevents"
)

func TestObserveContextRankDecisionShadowKeepsOriginalRanking(t *testing.T) {
	engine := decision.New(nil, heuristic.New(), decision.Config{})
	events := runtimeevents.NewStore(filepath.Join(t.TempDir(), "events"))
	root := map[string]any{
		"rankedFiles": []map[string]any{
			{"path": "auth/login.go", "score": 10, "reasons": []string{"semantic-symbol:login"}},
			{"path": "gateway/session.go", "score": 7, "reasons": []string{"graph-neighbor:auth"}},
			{"path": "video/render.go", "score": 1},
		},
	}
	summary := observeContextRankDecisionWithStore(
		context.Background(),
		engine,
		decisionruntime.ModeShadow,
		events,
		"session",
		"workspace",
		"task",
		"fix OAuth login",
		root,
	)
	if summary == nil || summary["baselineTop"] != "auth/login.go" || summary["candidateTop"] != "auth/login.go" || summary["top1Agreement"] != true {
		t.Fatalf("unexpected context shadow summary: %#v", summary)
	}
	ranked := contextRankedFiles(root)
	if ranked[0]["path"] != "auth/login.go" {
		t.Fatalf("shadow mode mutated ranked files: %#v", ranked)
	}
	stored, err := events.List("workspace", "task", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Type != decisionShadowContextRankEvent {
		t.Fatalf("expected context shadow event, got %#v", stored)
	}
}

func TestTopKOverlap(t *testing.T) {
	got := topKOverlap(
		[]string{"a", "b", "c", "d", "e"},
		[]string{"a", "x", "c", "y", "e"},
		5,
	)
	if got != 0.6 {
		t.Fatalf("unexpected overlap: %v", got)
	}
}
