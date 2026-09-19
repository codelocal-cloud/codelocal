package mcpgateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
	"github.com/0xmarkhydra/codelocal/internal/runtimeevents"
)

const decisionShadowContextRankEvent = "decision.shadow.context_rank"

func contextRankedFiles(root map[string]any) []map[string]any {
	if root == nil {
		return nil
	}
	if typed, ok := root["rankedFiles"].([]map[string]any); ok {
		return typed
	}
	raw, ok := root["rankedFiles"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if mapped, ok := item.(map[string]any); ok {
			out = append(out, mapped)
		}
	}
	return out
}

func contextRankCandidates(root map[string]any) ([]decision.Candidate, map[string]string, []string) {
	files := contextRankedFiles(root)
	if len(files) == 0 {
		return nil, nil, nil
	}
	if len(files) > 24 {
		files = files[:24]
	}
	maxScore := 0
	for _, file := range files {
		score := intValue(file["score"])
		if score > maxScore {
			maxScore = score
		}
	}
	if maxScore <= 0 {
		maxScore = 1
	}
	candidates := make([]decision.Candidate, 0, len(files))
	pathByID := make(map[string]string, len(files))
	baseline := make([]string, 0, len(files))
	for index, file := range files {
		path := strings.TrimSpace(fmt.Sprint(file["path"]))
		if path == "" {
			continue
		}
		id := fmt.Sprintf("f%02d", index)
		score := intValue(file["score"])
		reasons := stringSliceValue(file["reasons"])
		label := path
		if len(reasons) > 0 {
			if len(reasons) > 3 {
				reasons = reasons[:3]
			}
			label += "; evidence: " + strings.Join(reasons, ", ")
		}
		candidates = append(candidates, decision.Candidate{
			ID:    id,
			Label: label,
			Prior: float64(score) / float64(maxScore),
		})
		pathByID[id] = path
		baseline = append(baseline, path)
	}
	return candidates, pathByID, baseline
}

func topKOverlap(left, right []string, k int) float64 {
	if k <= 0 || len(left) == 0 || len(right) == 0 {
		return 0
	}
	if len(left) < k {
		k = len(left)
	}
	if len(right) < k {
		k = len(right)
	}
	if k == 0 {
		return 0
	}
	set := make(map[string]struct{}, k)
	for _, value := range left[:k] {
		set[value] = struct{}{}
	}
	matches := 0
	for _, value := range right[:k] {
		if _, ok := set[value]; ok {
			matches++
		}
	}
	return float64(matches) / float64(k)
}

func observeContextRankDecisionWithStore(
	ctx context.Context,
	engine *decision.Engine,
	mode decisionruntime.Mode,
	events *runtimeevents.Store,
	sessionID, workspaceKey, taskID, objective string,
	root map[string]any,
) map[string]any {
	if engine == nil || mode == decisionruntime.ModeOff {
		return nil
	}
	candidates, pathByID, baseline := contextRankCandidates(root)
	if len(candidates) < 2 || len(baseline) == 0 {
		return nil
	}
	result, err := engine.Rank(ctx, decision.RankRequest{
		Purpose:    "Rank these already-selected repository files by relevance to the task: " + strings.Join(strings.Fields(objective), " "),
		Candidates: candidates,
	})
	if err != nil {
		summary := map[string]any{
			"mode":           string(mode),
			"status":         "unavailable",
			"candidateCount": len(candidates),
			"baselineTop":    baseline[0],
		}
		appendDecisionShadowEvent(events, workspaceKey, taskID, sessionID, decisionShadowContextRankEvent, summary)
		return summary
	}
	ranked := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		if path := pathByID[item.ID]; path != "" {
			ranked = append(ranked, path)
		}
	}
	if len(ranked) == 0 {
		return nil
	}
	summary := map[string]any{
		"mode":           string(mode),
		"status":         "observed",
		"candidateCount": len(candidates),
		"baselineTop":    baseline[0],
		"candidateTop":   ranked[0],
		"top1Agreement":  baseline[0] == ranked[0],
		"top5Overlap":    topKOverlap(baseline, ranked, 5),
		"provider":       result.Provider,
		"confidence":     result.Confidence,
		"latencyMs":      result.LatencyMS,
		"fallback":       result.Fallback,
	}
	appendDecisionShadowEvent(events, workspaceKey, taskID, sessionID, decisionShadowContextRankEvent, summary)
	return summary
}

func appendDecisionShadowEvent(events *runtimeevents.Store, workspaceKey, taskID, sessionID, eventType string, payload map[string]any) {
	if events == nil || workspaceKey == "" || taskID == "" {
		return
	}
	_, _, _ = events.Append(workspaceKey, taskID, runtimeevents.Event{
		Type:      eventType,
		SessionID: sessionID,
		Payload:   payload,
	})
}

func (s *Service) observeContextRankDecision(
	ctx context.Context,
	sessionID, workspaceKey, taskID, objective string,
	root map[string]any,
) map[string]any {
	if s == nil {
		return nil
	}
	return observeContextRankDecisionWithStore(
		ctx,
		s.Decision,
		s.DecisionMode,
		decisionShadowEvents,
		sessionID,
		workspaceKey,
		taskID,
		objective,
		root,
	)
}
