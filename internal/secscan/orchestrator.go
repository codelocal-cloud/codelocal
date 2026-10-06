package secscan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type ScanRequest struct {
	Root    string
	Offline bool
}

type Engine interface {
	Name() string
	Version(context.Context) string
	Scan(context.Context, ScanRequest) ([]Finding, error)
}

type Orchestrator struct {
	Engines []Engine
	Now     func() time.Time
}

func NewDefaultOrchestrator() Orchestrator {
	return Orchestrator{
		Engines: []Engine{
			GitleaksEngine{},
			TrivyEngine{},
			SemgrepEngine{},
		},
		Now: time.Now,
	}
}

func (o Orchestrator) Scan(ctx context.Context, request ScanRequest) (Report, error) {
	root, err := filepath.Abs(request.Root)
	if err != nil {
		return Report{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return Report{}, err
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("security scan root is not a directory: %s", root)
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	started := now().UTC()
	report := Report{
		SchemaVersion:      1,
		FingerprintVersion: 2,
		ScanID:             "sec_" + started.Format("20060102T150405.000000000Z"),
		Project:            filepath.Base(root),
		Root:               root,
		StartedAt:          started,
		Offline:            request.Offline,
		Findings:           []Finding{},
		Engines:            []EngineStatus{},
		AI:                 AIState{Status: "not-run"},
	}
	report.Branch, report.Commit = gitMetadata(ctx, root)

	seen := map[string]struct{}{}
	occurrences := map[string]int{}
	successful := 0
	for _, engine := range o.Engines {
		status := EngineStatus{Name: engine.Name(), Version: engine.Version(ctx)}
		findings, scanErr := engine.Scan(ctx, ScanRequest{Root: root, Offline: request.Offline})
		if scanErr != nil {
			if errors.Is(scanErr, ErrUnavailable) {
				status.Status = "unavailable"
			} else {
				status.Status = "error"
			}
			status.Error = scanErr.Error()
			report.Engines = append(report.Engines, status)
			continue
		}
		successful++
		status.Status = "ok"
		for _, raw := range findings {
			if raw.Identity == "" {
				// ponytail: ordinal identity survives line shifts; use scanner-native IDs when all supported schemas expose them.
				identityKey := findingIdentityKey(root, raw)
				raw.Identity = fmt.Sprint(occurrences[identityKey])
				occurrences[identityKey]++
			}
			finding := finalizeFinding(root, raw)
			if _, exists := seen[finding.Fingerprint]; exists {
				continue
			}
			seen[finding.Fingerprint] = struct{}{}
			report.Findings = append(report.Findings, finding)
			status.Findings++
		}
		report.Engines = append(report.Engines, status)
	}
	report.FinishedAt = now().UTC()
	report.Recalculate()
	if successful == 0 {
		return report, fmt.Errorf("%w: install at least one supported scanner (gitleaks, trivy, semgrep)", ErrUnavailable)
	}
	return report, nil
}

func findingIdentityKey(root string, finding Finding) string {
	return strings.Join([]string{
		finding.Engine,
		finding.RuleID,
		finding.Category,
		relativeFindingPath(root, finding.File),
	}, "\x00")
}

func gitMetadata(ctx context.Context, root string) (string, string) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", ""
	}
	run := func(args ...string) string {
		command := exec.CommandContext(ctx, gitPath, append([]string{"-C", root}, args...)...)
		output, err := command.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(output))
	}
	return run("branch", "--show-current"), run("rev-parse", "--verify", "HEAD")
}
