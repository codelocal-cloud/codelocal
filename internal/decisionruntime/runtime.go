package decisionruntime

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/decision"
	"github.com/0xmarkhydra/codelocal/internal/decision/providers/heuristic"
	"github.com/0xmarkhydra/codelocal/internal/decision/providers/jev"
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeShadow Mode = "shadow"
	ModeActive Mode = "active"
)

type Runtime struct {
	Engine        *decision.Engine
	Mode          Mode
	Provider      string
	PrimaryReady  bool
	FallbackReady bool
}

func FromEnv() Runtime {
	jevConfig := jev.ConfigFromEnv()
	mode := modeFromEnv(jevConfig.APIKey != "")
	if mode == ModeOff {
		return Runtime{Mode: ModeOff}
	}

	fallback := heuristic.New()
	config := decision.Config{
		Timeout:       timeoutFromEnv(),
		MinConfidence: confidenceFromEnv(),
	}
	primary, err := jev.New(jevConfig)
	if err != nil {
		return Runtime{
			Engine:        decision.New(nil, fallback, config),
			Mode:          mode,
			Provider:      fallback.Name(),
			PrimaryReady:  false,
			FallbackReady: true,
		}
	}
	return Runtime{
		Engine:        decision.New(primary, fallback, config),
		Mode:          mode,
		Provider:      primary.Name(),
		PrimaryReady:  true,
		FallbackReady: true,
	}
}

func modeFromEnv(hasAPIKey bool) Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODELOCAL_DECISION_MODE"))) {
	case "off", "0", "false", "disabled":
		return ModeOff
	case "active", "on", "1", "true":
		return ModeActive
	case "shadow":
		return ModeShadow
	case "":
		return ModeOff
	default:
		return ModeOff
	}
}

func timeoutFromEnv() time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CODELOCAL_DECISION_TIMEOUT_MS")))
	if err != nil || value < 50 || value > 5000 {
		return 750 * time.Millisecond
	}
	return time.Duration(value) * time.Millisecond
}

func confidenceFromEnv() float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv("CODELOCAL_DECISION_MIN_CONFIDENCE")), 64)
	if err != nil || value < 0 || value > 1 {
		return 0.8
	}
	return value
}
