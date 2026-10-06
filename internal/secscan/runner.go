package secscan

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

var supportedEngineNames = []string{"gitleaks", "semgrep", "trivy"}

func ValidateEngineNames(names []string) error {
	if len(names) == 0 {
		return nil
	}
	supported := make(map[string]struct{}, len(supportedEngineNames))
	for _, name := range supportedEngineNames {
		supported[name] = struct{}{}
	}
	var unknown []string
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := supported[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("unknown security engine(s): %s; supported engines: %s", strings.Join(unknown, ", "), strings.Join(supportedEngineNames, ", "))
}

func Run(ctx context.Context, options Options) (Report, error) {
	if err := ValidateEngineNames(options.Engines); err != nil {
		return Report{}, err
	}
	orchestrator := NewDefaultOrchestrator()
	if len(options.Engines) > 0 {
		selected := make(map[string]bool, len(options.Engines))
		for _, name := range options.Engines {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				selected[name] = true
			}
		}
		filtered := make([]Engine, 0, len(orchestrator.Engines))
		for _, engine := range orchestrator.Engines {
			if selected[engine.Name()] {
				filtered = append(filtered, engine)
			}
		}
		orchestrator.Engines = filtered
	}
	return orchestrator.Scan(ctx, ScanRequest{Root: options.Root, Offline: options.Offline})
}
