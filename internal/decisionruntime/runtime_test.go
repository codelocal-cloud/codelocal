package decisionruntime

import (
	"testing"
	"time"
)

func TestModeRequiresExplicitEnableEvenWithKey(t *testing.T) {
	t.Setenv("CODELOCAL_DECISION_MODE", "")
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	if got := FromEnv().Mode; got != ModeOff {
		t.Fatalf("expected explicit opt-in, got %q", got)
	}

	t.Setenv("CODELOCAL_DECISION_MODE", "shadow")
	runtime := FromEnv()
	if runtime.Mode != ModeShadow || !runtime.PrimaryReady || runtime.Provider != "jev" {
		t.Fatalf("expected managed Jev shadow runtime, got %#v", runtime)
	}
}

func TestExplicitActiveFallsBackWhenJevUnavailable(t *testing.T) {
	t.Setenv("CODELOCAL_DECISION_MODE", "active")
	t.Setenv("TYPESAFE_API_KEY", "")
	runtime := FromEnv()
	if runtime.Mode != ModeActive || runtime.Engine == nil || runtime.PrimaryReady || !runtime.FallbackReady || runtime.Provider != "heuristic" {
		t.Fatalf("unexpected fallback runtime: %#v", runtime)
	}
}

func TestRuntimeSettingsAreBounded(t *testing.T) {
	t.Setenv("CODELOCAL_DECISION_TIMEOUT_MS", "120")
	if got := timeoutFromEnv(); got != 120*time.Millisecond {
		t.Fatalf("unexpected timeout: %s", got)
	}
	t.Setenv("CODELOCAL_DECISION_TIMEOUT_MS", "999999")
	if got := timeoutFromEnv(); got != 750*time.Millisecond {
		t.Fatalf("expected bounded default timeout, got %s", got)
	}

	t.Setenv("CODELOCAL_DECISION_MIN_CONFIDENCE", "0.93")
	if got := confidenceFromEnv(); got != 0.93 {
		t.Fatalf("unexpected confidence: %v", got)
	}
	t.Setenv("CODELOCAL_DECISION_MIN_CONFIDENCE", "2")
	if got := confidenceFromEnv(); got != 0.8 {
		t.Fatalf("expected bounded default confidence, got %v", got)
	}
}
