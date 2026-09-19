package heuristic

import (
	"context"
	"sort"
	"strings"

	"github.com/0xmarkhydra/codelocal/internal/decision"
)

type Provider struct{}

func New() Provider { return Provider{} }

func (Provider) Name() string { return "heuristic" }

func (Provider) Boolean(_ context.Context, req decision.BooleanRequest) (decision.BooleanResult, error) {
	score := 0.5
	if req.Prior != nil {
		score = clamp(*req.Prior)
	}
	value := score >= 0.5
	confidence := score
	if !value {
		confidence = 1 - score
	}
	return decision.BooleanResult{
		Value: value,
		Meta: decision.Meta{
			Provider:   "heuristic",
			Confidence: confidence,
			ReasonCode: "prior_probability",
		},
	}, nil
}

func (Provider) Choice(_ context.Context, req decision.ChoiceRequest) (decision.ChoiceResult, error) {
	if len(req.Candidates) == 0 {
		return decision.ChoiceResult{}, decision.ErrInvalidResult
	}
	best := decision.Candidate{}
	hasBest := false
	scores := make(map[string]float64, len(req.Candidates))
	for _, candidate := range req.Candidates {
		candidate = normalizedCandidate(candidate)
		if candidate.ID == "" {
			continue
		}
		scores[candidate.ID] = candidate.Prior
		if !hasBest || candidate.Prior > best.Prior || (candidate.Prior == best.Prior && candidate.ID < best.ID) {
			best = candidate
			hasBest = true
		}
	}
	if !hasBest {
		return decision.ChoiceResult{}, decision.ErrInvalidResult
	}
	return decision.ChoiceResult{
		ChoiceID: best.ID,
		Scores:   scores,
		Meta: decision.Meta{
			Provider:   "heuristic",
			Confidence: best.Prior,
			ReasonCode: "highest_prior",
		},
	}, nil
}

func (Provider) Score(_ context.Context, req decision.ScoreRequest) (decision.ScoreResult, error) {
	item := normalizedCandidate(req.Item)
	if item.ID == "" {
		return decision.ScoreResult{}, decision.ErrInvalidResult
	}
	return decision.ScoreResult{
		Score: item.Prior,
		Meta: decision.Meta{
			Provider:   "heuristic",
			Confidence: confidenceFromScore(item.Prior),
			ReasonCode: "candidate_prior",
		},
	}, nil
}

func (Provider) Rank(_ context.Context, req decision.RankRequest) (decision.RankResult, error) {
	if len(req.Candidates) == 0 {
		return decision.RankResult{}, decision.ErrInvalidResult
	}
	items := make([]decision.RankedCandidate, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		candidate = normalizedCandidate(candidate)
		if candidate.ID == "" {
			continue
		}
		items = append(items, decision.RankedCandidate{ID: candidate.ID, Score: candidate.Prior})
	}
	if len(items) == 0 {
		return decision.RankResult{}, decision.ErrInvalidResult
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ID < items[j].ID
	})
	return decision.RankResult{
		Items: items,
		Meta: decision.Meta{
			Provider:   "heuristic",
			Confidence: items[0].Score,
			ReasonCode: "prior_ranking",
		},
	}, nil
}

func normalizedCandidate(candidate decision.Candidate) decision.Candidate {
	candidate.ID = strings.TrimSpace(candidate.ID)
	candidate.Prior = clamp(candidate.Prior)
	return candidate
}

func clamp(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func confidenceFromScore(score float64) float64 {
	if score >= 0.5 {
		return score
	}
	return 1 - score
}
