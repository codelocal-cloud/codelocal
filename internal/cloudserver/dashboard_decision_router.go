package cloudserver

import (
	"context"
	"fmt"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
)

type dashboardModelDecisionShadow struct {
	Mode       decisionruntime.Mode `json:"mode"`
	Baseline   string               `json:"baseline"`
	Candidate  string               `json:"candidate,omitempty"`
	Agreement  bool                 `json:"agreement"`
	Provider   string               `json:"provider,omitempty"`
	Confidence float64              `json:"confidence,omitempty"`
	LatencyMS  int64                `json:"latencyMs,omitempty"`
	Fallback   bool                 `json:"fallback,omitempty"`
	Status     string               `json:"status"`
}

func dashboardModelDecisionCandidates(route []dashboardLLMTarget) ([]decision.Candidate, map[string]string) {
	if len(route) > 8 {
		route = route[:8]
	}
	out := make([]decision.Candidate, 0, len(route))
	modelByID := make(map[string]string, len(route))
	for index, target := range route {
		id := fmt.Sprintf("m%02d", index)
		prior := 1.0 - float64(index)*0.1
		if prior < 0.1 {
			prior = 0.1
		}
		label := "model=" + target.Model
		if target.Community {
			label += "; community"
		} else {
			label += "; managed/private"
		}
		if target.Vision {
			label += "; vision"
		} else {
			label += "; text"
		}
		out = append(out, decision.Candidate{ID: id, Label: label, Prior: prior})
		modelByID[id] = target.Model
	}
	return out, modelByID
}

func observeDashboardModelDecision(ctx context.Context, s *Server, userID string, route []dashboardLLMTarget) *dashboardModelDecisionShadow {
	if s == nil || s.MCP == nil || s.MCP.Decision == nil || len(route) < 2 {
		return nil
	}
	prefs := s.MCP.DecisionPreferences(ctx, userID)
	if !prefs.Model || prefs.Mode == decisionruntime.ModeOff {
		return nil
	}
	candidates, modelByID := dashboardModelDecisionCandidates(route)
	result, err := s.MCP.Decision.Choice(ctx, decision.ChoiceRequest{
		Purpose:    "Choose the most appropriate configured model route for a general CodeLocal chat request using only provider capabilities and existing route priority.",
		Candidates: candidates,
	})
	shadow := &dashboardModelDecisionShadow{
		Mode:     prefs.Mode,
		Baseline: route[0].Model,
		Status:   "unavailable",
	}
	if err != nil {
		return shadow
	}
	shadow.Status = "observed"
	shadow.Candidate = modelByID[result.ChoiceID]
	shadow.Agreement = shadow.Candidate == shadow.Baseline
	shadow.Provider = result.Provider
	shadow.Confidence = result.Confidence
	shadow.LatencyMS = result.LatencyMS
	shadow.Fallback = result.Fallback
	return shadow
}
