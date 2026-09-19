package decisionruntime

import "testing"

func TestPreferencesFromValues(t *testing.T) {
	fallback := DefaultPreferences(ModeShadow)
	got := PreferencesFromValues(map[string]string{
		ConfigMode:     "active",
		ConfigContext:  "off",
		ConfigComputer: "on",
	}, fallback)
	if got.Mode != ModeActive || got.Context || !got.Computer || !got.Route {
		t.Fatalf("unexpected preferences: %#v", got)
	}
}

func TestPreferencesOffDisablesAllSurfaces(t *testing.T) {
	got := PreferencesFromValues(map[string]string{ConfigMode: "off"}, DefaultPreferences(ModeShadow))
	if got.Mode != ModeOff || got.Route || got.Context || got.Output || got.Model || got.Review || got.Brain || got.Computer {
		t.Fatalf("off mode must disable all surfaces: %#v", got)
	}
}
