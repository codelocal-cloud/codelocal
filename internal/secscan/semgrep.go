package secscan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SemgrepEngine struct{}

type semgrepReport struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
		} `json:"start"`
		End struct {
			Line int `json:"line"`
		} `json:"end"`
		Extra struct {
			Message  string `json:"message"`
			Severity string `json:"severity"`
			Fix      string `json:"fix"`
		} `json:"extra"`
	} `json:"results"`
}

func (SemgrepEngine) Name() string { return "semgrep" }

func (SemgrepEngine) Version(ctx context.Context) string {
	return scannerVersion(ctx, "semgrep", "--version")
}

func (SemgrepEngine) Scan(ctx context.Context, request ScanRequest) ([]Finding, error) {
	config := strings.TrimSpace(os.Getenv("CODELOCAL_SEMGREP_CONFIG"))
	if config == "" {
		if request.Offline {
			return nil, fmt.Errorf("%w: semgrep needs CODELOCAL_SEMGREP_CONFIG pointing to local rules in offline mode", ErrUnavailable)
		}
		config = "auto"
	} else if request.Offline {
		configPath, err := filepath.Abs(config)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid local semgrep config: %v", ErrUnavailable, err)
		}
		if _, err := os.Stat(configPath); err != nil {
			return nil, fmt.Errorf("%w: offline semgrep config must be a readable local path: %v", ErrUnavailable, err)
		}
		config = configPath
	}
	result, err := runCommand(ctx, "semgrep",
		"scan",
		"--json",
		"--quiet",
		"--metrics", "off",
		"--disable-version-check",
		"--config", config,
		request.Root,
	)
	if err != nil {
		return nil, err
	}
	return parseSemgrepReport(result.Stdout)
}

func parseSemgrepReport(raw []byte) ([]Finding, error) {
	var report semgrepReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode semgrep report: %w", err)
	}
	findings := make([]Finding, 0, len(report.Results))
	for _, item := range report.Results {
		findings = append(findings, Finding{
			Engine:         "semgrep",
			RuleID:         item.CheckID,
			Category:       "source-code",
			Severity:       NormalizeSeverity(item.Extra.Severity),
			Title:          item.CheckID,
			Description:    item.Extra.Message,
			Recommendation: item.Extra.Fix,
			File:           item.Path,
			StartLine:      item.Start.Line,
			EndLine:        item.End.Line,
		})
	}
	return findings, nil
}
