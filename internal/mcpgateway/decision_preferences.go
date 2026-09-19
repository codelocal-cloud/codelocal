package mcpgateway

import (
	"context"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
	"github.com/0xmarkhydra/codelocal/internal/decisionruntime"
)

func (s *Service) decisionPreferences(ctx context.Context, userID string) decisionruntime.Preferences {
	if s == nil || s.Decision == nil || s.DecisionMode == decisionruntime.ModeOff {
		return decisionruntime.DefaultPreferences(decisionruntime.ModeOff)
	}
	base := decisionruntime.DefaultPreferences(s.DecisionMode)
	if s.Store == nil || userID == "" {
		return base
	}
	layer, err := s.Store.RuntimeSettingsLayer(ctx, userID, cloud.RuntimeScopeGlobal, "", "")
	if err != nil {
		return base
	}
	prefs := decisionruntime.PreferencesFromValues(layer.Values, base)
	if s.DecisionMode == decisionruntime.ModeShadow && prefs.Mode == decisionruntime.ModeActive {
		prefs.Mode = decisionruntime.ModeShadow
	}
	return prefs
}

func (s *Service) DecisionPreferences(ctx context.Context, userID string) decisionruntime.Preferences {
	return s.decisionPreferences(ctx, userID)
}

func (s *Service) DecisionStatus(ctx context.Context, userID string) map[string]any {
	prefs := s.decisionPreferences(ctx, userID)
	provider := s.DecisionProvider
	if provider == "" {
		provider = "unavailable"
	}
	return map[string]any{
		"mode":         prefs.Mode,
		"provider":     provider,
		"primaryReady": s.DecisionPrimaryReady,
		"managed":      true,
		"features": map[string]bool{
			"route":    prefs.Route,
			"context":  prefs.Context,
			"output":   prefs.Output,
			"model":    prefs.Model,
			"review":   prefs.Review,
			"brain":    prefs.Brain,
			"computer": prefs.Computer,
		},
	}
}
