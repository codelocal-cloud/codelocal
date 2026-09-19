package mcpgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
	"github.com/0xmarkhydra/codelocal/internal/runtimeevents"
)

const (
	decisionShadowOutputEvent   = "decision.shadow.output"
	decisionShadowReviewEvent   = "decision.shadow.review"
	decisionShadowBrainEvent    = "decision.shadow.brain"
	decisionShadowComputerEvent = "decision.shadow.computer"
)

func decisionSensitiveText(value string) bool {
	lower := strings.ToLower(value)
	for _, pattern := range []string{"authorization: bearer", "private key", "api_key", "apikey", "access_token", "refresh_token", "client_secret", "password", "secret=", "token=", ".env"} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func observeToolOutputDecisionWithStore(ctx context.Context, engine *decision.Engine, mode decisionruntime.Mode, events *runtimeevents.Store, sessionID, workspaceKey, taskID, objective, operation string, root map[string]any) map[string]any {
	if engine == nil || mode == decisionruntime.ModeOff || root == nil {
		return nil
	}
	raw, err := json.Marshal(root)
	if err != nil || len(raw) < 4096 {
		return nil
	}
	summary := map[string]any{"mode": string(mode), "operation": operation, "bytes": len(raw)}
	text := string(raw)
	if decisionSensitiveText(text) {
		summary["status"] = "skipped_sensitive"
		appendDecisionShadowEvent(events, workspaceKey, taskID, sessionID, decisionShadowOutputEvent, summary)
		return summary
	}
	if len(text) > 1800 {
		text = text[:1800]
	}
	purpose := "Decide whether this large tool-output excerpt is useful for completing the task. Operation: " + operation
	if objective = strings.Join(strings.Fields(objective), " "); objective != "" {
		if len(objective) > 400 {
			objective = objective[:400]
		}
		purpose += ". Task: " + objective
	}
	result, callErr := engine.Boolean(ctx, decision.BooleanRequest{Purpose: purpose, Input: text})
	if callErr != nil {
		summary["status"] = "unavailable"
	} else {
		summary["status"] = "observed"
		summary["useful"] = result.Value
		summary["provider"] = result.Provider
		summary["confidence"] = result.Confidence
		summary["latencyMs"] = result.LatencyMS
		summary["fallback"] = result.Fallback
	}
	appendDecisionShadowEvent(events, workspaceKey, taskID, sessionID, decisionShadowOutputEvent, summary)
	return summary
}

func reviewCandidates(paths []string) []decision.Candidate {
	seen := map[string]struct{}{}
	out := make([]decision.Candidate, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		lower := strings.ToLower(path)
		prior := 0.5
		switch {
		case strings.Contains(lower, "security"), strings.Contains(lower, "auth"), strings.Contains(lower, "oauth"), strings.Contains(lower, "token"), strings.Contains(lower, "permission"):
			prior = 0.9
		case strings.Contains(lower, "test"), strings.HasSuffix(lower, ".md"):
			prior = 0.35
		}
		out = append(out, decision.Candidate{ID: fmt.Sprintf("r%02d", len(out)), Label: path, Prior: prior})
		if len(out) == 24 {
			break
		}
	}
	return out
}

func observeReviewDecisionWithStore(ctx context.Context, engine *decision.Engine, mode decisionruntime.Mode, events *runtimeevents.Store, sessionID, workspaceKey, taskID string, paths []string) map[string]any {
	candidates := reviewCandidates(paths)
	if engine == nil || mode == decisionruntime.ModeOff || len(candidates) < 2 {
		return nil
	}
	result, err := engine.Rank(ctx, decision.RankRequest{Purpose: "Rank changed files by correctness, security and reliability review risk.", Candidates: candidates})
	summary := map[string]any{"mode": string(mode), "candidateCount": len(candidates)}
	if err != nil || len(result.Items) == 0 {
		summary["status"] = "unavailable"
	} else {
		labels := map[string]string{}
		for _, candidate := range candidates {
			labels[candidate.ID] = candidate.Label
		}
		summary["status"] = "observed"
		summary["highestRisk"] = labels[result.Items[0].ID]
		summary["provider"] = result.Provider
		summary["confidence"] = result.Confidence
		summary["latencyMs"] = result.LatencyMS
		summary["fallback"] = result.Fallback
	}
	appendDecisionShadowEvent(events, workspaceKey, taskID, sessionID, decisionShadowReviewEvent, summary)
	return summary
}

func brainRuleCandidates(root map[string]any) []decision.Candidate {
	brain := nestedMap(root["projectBrain"])
	raw, ok := brain["effectiveRules"].([]any)
	if !ok {
		if typed, ok2 := brain["effectiveRules"].([]map[string]any); ok2 {
			raw = make([]any, len(typed))
			for i := range typed {
				raw[i] = typed[i]
			}
		}
	}
	out := []decision.Candidate{}
	maxRank := 1
	items := []map[string]any{}
	for _, value := range raw {
		item, _ := value.(map[string]any)
		if item == nil {
			continue
		}
		items = append(items, item)
		if rank := intValue(item["authorityRank"]); rank > maxRank {
			maxRank = rank
		}
	}
	for _, item := range items {
		id := strings.TrimSpace(fmt.Sprint(item["id"]))
		if id == "" {
			continue
		}
		label := strings.TrimSpace(fmt.Sprint(item["sourcePath"])) + "; authority=" + strings.TrimSpace(fmt.Sprint(item["authority"])) + "; lane=" + strings.TrimSpace(fmt.Sprint(item["lane"]))
		out = append(out, decision.Candidate{ID: id, Label: label, Prior: float64(intValue(item["authorityRank"])) / float64(maxRank)})
		if len(out) == 24 {
			break
		}
	}
	return out
}

func observeBrainDecisionWithStore(ctx context.Context, engine *decision.Engine, mode decisionruntime.Mode, events *runtimeevents.Store, sessionID, workspaceKey, taskID string, root map[string]any) map[string]any {
	candidates := brainRuleCandidates(root)
	if engine == nil || mode == decisionruntime.ModeOff || len(candidates) < 2 {
		return nil
	}
	result, err := engine.Rank(ctx, decision.RankRequest{Purpose: "Rank Project Brain rules by likely relevance while preserving mandatory-rule authority.", Candidates: candidates})
	summary := map[string]any{"mode": string(mode), "candidateCount": len(candidates)}
	if err != nil || len(result.Items) == 0 {
		summary["status"] = "unavailable"
	} else {
		summary["status"] = "observed"
		summary["candidateTop"] = result.Items[0].ID
		summary["provider"] = result.Provider
		summary["confidence"] = result.Confidence
		summary["latencyMs"] = result.LatencyMS
		summary["fallback"] = result.Fallback
	}
	appendDecisionShadowEvent(events, workspaceKey, taskID, sessionID, decisionShadowBrainEvent, summary)
	return summary
}

func observeComputerDecisionWithStore(ctx context.Context, engine *decision.Engine, mode decisionruntime.Mode, events *runtimeevents.Store, sessionID, workspaceKey, taskID, operation string, args map[string]any) map[string]any {
	if engine == nil || mode == decisionruntime.ModeOff || !strings.HasPrefix(operation, "computer.") {
		return nil
	}
	target := strings.TrimSpace(fmt.Sprint(args["target"]))
	windowID := strings.TrimSpace(fmt.Sprint(args["windowId"]))
	if target == "" || target == "<nil>" {
		return nil
	}
	input := "operation=" + operation + "; target=" + target
	if windowID != "" && windowID != "<nil>" {
		input += "; window scoped"
	}
	result, err := engine.Boolean(ctx, decision.BooleanRequest{Purpose: "Is this semantic desktop target sufficiently specific for a bounded action?", Input: input})
	summary := map[string]any{"mode": string(mode), "operation": operation, "target": target}
	if err != nil {
		summary["status"] = "unavailable"
	} else {
		summary["status"] = "observed"
		summary["specific"] = result.Value
		summary["provider"] = result.Provider
		summary["confidence"] = result.Confidence
		summary["latencyMs"] = result.LatencyMS
		summary["fallback"] = result.Fallback
	}
	appendDecisionShadowEvent(events, workspaceKey, taskID, sessionID, decisionShadowComputerEvent, summary)
	return summary
}
