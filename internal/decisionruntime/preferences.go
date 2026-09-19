package decisionruntime

import "strings"

const (
	ConfigMode     = "CODELOCAL_DECISION_MODE"
	ConfigRoute    = "CODELOCAL_DECISION_ROUTE"
	ConfigContext  = "CODELOCAL_DECISION_CONTEXT"
	ConfigOutput   = "CODELOCAL_DECISION_OUTPUT"
	ConfigModel    = "CODELOCAL_DECISION_MODEL"
	ConfigReview   = "CODELOCAL_DECISION_REVIEW"
	ConfigBrain    = "CODELOCAL_DECISION_BRAIN"
	ConfigComputer = "CODELOCAL_DECISION_COMPUTER"
)

type Preferences struct {
	Mode     Mode `json:"mode"`
	Route    bool `json:"route"`
	Context  bool `json:"context"`
	Output   bool `json:"output"`
	Model    bool `json:"model"`
	Review   bool `json:"review"`
	Brain    bool `json:"brain"`
	Computer bool `json:"computer"`
}

func DefaultPreferences(mode Mode) Preferences {
	enabled := mode != ModeOff
	return Preferences{
		Mode:  mode,
		Route: enabled, Context: enabled, Output: enabled,
		Model: enabled, Review: enabled, Brain: enabled, Computer: enabled,
	}
}

func PreferencesFromValues(values map[string]string, fallback Preferences) Preferences {
	out := fallback
	if values == nil {
		return out
	}
	if raw, ok := values[ConfigMode]; ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "off", "disabled", "false", "0":
			out.Mode = ModeOff
		case "shadow", "automatic", "auto":
			out.Mode = ModeShadow
		case "active", "on", "true", "1":
			out.Mode = ModeActive
		}
	}
	out.Route = boolSetting(values, ConfigRoute, out.Route)
	out.Context = boolSetting(values, ConfigContext, out.Context)
	out.Output = boolSetting(values, ConfigOutput, out.Output)
	out.Model = boolSetting(values, ConfigModel, out.Model)
	out.Review = boolSetting(values, ConfigReview, out.Review)
	out.Brain = boolSetting(values, ConfigBrain, out.Brain)
	out.Computer = boolSetting(values, ConfigComputer, out.Computer)
	if out.Mode == ModeOff {
		out.Route, out.Context, out.Output = false, false, false
		out.Model, out.Review, out.Brain, out.Computer = false, false, false, false
	}
	return out
}

func boolSetting(values map[string]string, key string, fallback bool) bool {
	raw, ok := values[key]
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "on", "yes", "enabled":
		return true
	case "0", "false", "off", "no", "disabled":
		return false
	default:
		return fallback
	}
}
