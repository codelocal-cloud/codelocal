package decision_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decision/providers/heuristic"
)

type fakeProvider struct {
	name   string
	choice decision.ChoiceResult
	err    error
	delay  time.Duration
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Boolean(context.Context, decision.BooleanRequest) (decision.BooleanResult, error) {
	return decision.BooleanResult{}, f.err
}
func (f fakeProvider) Choice(ctx context.Context, _ decision.ChoiceRequest) (decision.ChoiceResult, error) {
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return decision.ChoiceResult{}, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	return f.choice, f.err
}
func (f fakeProvider) Score(context.Context, decision.ScoreRequest) (decision.ScoreResult, error) {
	return decision.ScoreResult{}, f.err
}
func (f fakeProvider) Rank(context.Context, decision.RankRequest) (decision.RankResult, error) {
	return decision.RankResult{}, f.err
}

func TestChoiceUsesPrimaryWhenConfidenceIsEnough(t *testing.T) {
	engine := decision.New(
		fakeProvider{name: "primary", choice: decision.ChoiceResult{
			ChoiceID: "code",
			Meta:     decision.Meta{Confidence: 0.92},
		}},
		heuristic.New(),
		decision.Config{MinConfidence: 0.8},
	)

	result, err := engine.Choice(context.Background(), decision.ChoiceRequest{
		Candidates: []decision.Candidate{{ID: "code", Prior: 0.4}, {ID: "shell", Prior: 0.6}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChoiceID != "code" || result.Provider != "primary" || result.Fallback {
		t.Fatalf("unexpected primary result: %#v", result)
	}
}

func TestChoiceFallsBackOnProviderError(t *testing.T) {
	engine := decision.New(
		fakeProvider{name: "primary", err: errors.New("boom")},
		heuristic.New(),
		decision.Config{MinConfidence: 0.8},
	)

	result, err := engine.Choice(context.Background(), decision.ChoiceRequest{
		Candidates: []decision.Candidate{{ID: "code", Prior: 0.9}, {ID: "shell", Prior: 0.1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChoiceID != "code" || result.Provider != "heuristic" || !result.Fallback {
		t.Fatalf("unexpected fallback result: %#v", result)
	}
}

func TestChoiceFallsBackWhenPrimaryConfidenceIsLow(t *testing.T) {
	engine := decision.New(
		fakeProvider{name: "primary", choice: decision.ChoiceResult{
			ChoiceID: "shell",
			Meta:     decision.Meta{Confidence: 0.51},
		}},
		heuristic.New(),
		decision.Config{MinConfidence: 0.8},
	)

	result, err := engine.Choice(context.Background(), decision.ChoiceRequest{
		Candidates: []decision.Candidate{{ID: "code", Prior: 0.95}, {ID: "shell", Prior: 0.05}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChoiceID != "code" || !result.Fallback {
		t.Fatalf("expected heuristic fallback, got %#v", result)
	}
}

func TestHeuristicRankIsDeterministic(t *testing.T) {
	provider := heuristic.New()
	result, err := provider.Rank(context.Background(), decision.RankRequest{
		Candidates: []decision.Candidate{
			{ID: "b", Prior: 0.7},
			{ID: "a", Prior: 0.7},
			{ID: "c", Prior: 0.2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 || result.Items[0].ID != "a" || result.Items[1].ID != "b" || result.Items[2].ID != "c" {
		t.Fatalf("unexpected ranking: %#v", result.Items)
	}
}

func TestChoiceFallsBackOnPrimaryTimeout(t *testing.T) {
	engine := decision.New(
		fakeProvider{name: "slow", delay: 50 * time.Millisecond, choice: decision.ChoiceResult{
			ChoiceID: "shell",
			Meta:     decision.Meta{Confidence: 0.99},
		}},
		heuristic.New(),
		decision.Config{Timeout: 5 * time.Millisecond, MinConfidence: 0.8},
	)

	result, err := engine.Choice(context.Background(), decision.ChoiceRequest{
		Candidates: []decision.Candidate{{ID: "code", Prior: 0.9}, {ID: "shell", Prior: 0.1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChoiceID != "code" || !result.Fallback || result.Provider != "heuristic" {
		t.Fatalf("expected timeout fallback, got %#v", result)
	}
}

func TestChoiceRejectsInvalidFallbackResult(t *testing.T) {
	engine := decision.New(
		fakeProvider{name: "primary", err: errors.New("boom")},
		fakeProvider{name: "bad-fallback", choice: decision.ChoiceResult{
			ChoiceID: "not-a-candidate",
			Meta:     decision.Meta{Confidence: 0.99},
		}},
		decision.Config{},
	)

	_, err := engine.Choice(context.Background(), decision.ChoiceRequest{
		Candidates: []decision.Candidate{{ID: "code", Prior: 0.9}},
	})
	if !errors.Is(err, decision.ErrInvalidResult) {
		t.Fatalf("expected invalid result error, got %v", err)
	}
}

func TestHeuristicChoiceSkipsBlankCandidateIDs(t *testing.T) {
	provider := heuristic.New()
	result, err := provider.Choice(context.Background(), decision.ChoiceRequest{
		Candidates: []decision.Candidate{{ID: "", Prior: 1}, {ID: "code", Prior: 0.7}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChoiceID != "code" {
		t.Fatalf("expected valid candidate to win, got %#v", result)
	}
}
