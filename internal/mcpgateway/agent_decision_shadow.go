package mcpgateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
	"github.com/0xmarkhydra/codelocal/internal/orchestration"
	"github.com/0xmarkhydra/codelocal/internal/runtimeevents"
)

const decisionShadowRouteEvent = "decision.shadow.route"

var decisionShadowEvents = runtimeevents.NewStore("")

func routeDecisionCandidates(route orchestration.Decision) []decision.Candidate {
	order := []orchestration.Lane{
		orchestration.LaneCode,
		orchestration.LaneShell,
		orchestration.LaneBrowser,
		orchestration.LaneComputer,
	}
	maxScore := 0
	for _, score := range route.Scores {
		if score > maxScore {
			maxScore = score
		}
	}
	if maxScore <= 0 {
		maxScore = 1
	}
	out := make([]decision.Candidate, 0, len(order))
	for _, lane := range order {
		score, ok := route.Scores[lane]
		if !ok || score <= 0 {
			continue
		}
		out = append(out, decision.Candidate{
			ID:    string(lane),
			Label: fmt.Sprintf("%s execution lane; deterministic evidence score %d", lane, score),
			Prior: float64(score) / float64(maxScore),
		})
	}
	return out
}

func decisionRoutePurpose(objective string) string {
	objective = strings.Join(strings.Fields(objective), " ")
	if len(objective) > 1200 {
		objective = objective[:1200]
	}
	if objective == "" {
		return "Choose the best CodeLocal execution lane from the bounded candidates."
	}
	return "Choose the best CodeLocal execution lane for this task: " + objective
}

func observeRouteDecisionWithStore(
	ctx context.Context,
	engine *decision.Engine,
	mode decisionruntime.Mode,
	events *runtimeevents.Store,
	sessionID, workspaceKey, taskID, objective string,
	route orchestration.Decision,
) map[string]any {
	if engine == nil || mode == decisionruntime.ModeOff || route.Primary == orchestration.LaneNone {
		return nil
	}
	candidates := routeDecisionCandidates(route)
	if len(candidates) == 0 {
		return nil
	}
	result, err := engine.Choice(ctx, decision.ChoiceRequest{
		Purpose:    decisionRoutePurpose(objective),
		Candidates: candidates,
	})
	if err != nil {
		summary := map[string]any{
			"mode":     string(mode),
			"status":   "unavailable",
			"baseline": string(route.Primary),
		}
		if events != nil && workspaceKey != "" && taskID != "" {
			_, _, _ = events.Append(workspaceKey, taskID, runtimeevents.Event{
				Type:      decisionShadowRouteEvent,
				SessionID: sessionID,
				Payload:   summary,
			})
		}
		return summary
	}

	summary := map[string]any{
		"mode":       string(mode),
		"status":     "observed",
		"baseline":   string(route.Primary),
		"candidate":  result.ChoiceID,
		"agreement":  result.ChoiceID == string(route.Primary),
		"provider":   result.Provider,
		"confidence": result.Confidence,
		"latencyMs":  result.LatencyMS,
		"fallback":   result.Fallback,
	}
	if events != nil && workspaceKey != "" && taskID != "" {
		_, _, _ = events.Append(workspaceKey, taskID, runtimeevents.Event{
			Type:      decisionShadowRouteEvent,
			SessionID: sessionID,
			Payload:   summary,
		})
	}
	return summary
}

func (s *Service) observeRouteDecision(
	ctx context.Context,
	sessionID, workspaceKey, taskID, objective string,
	route orchestration.Decision,
) map[string]any {
	if s == nil {
		return nil
	}
	return observeRouteDecisionWithStore(
		ctx,
		s.Decision,
		s.DecisionMode,
		decisionShadowEvents,
		sessionID,
		workspaceKey,
		taskID,
		objective,
		route,
	)
}
