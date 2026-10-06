package secscan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

type GitleaksEngine struct{}

type gitleaksResult struct {
	RuleID      string `json:"RuleID"`
	Description string `json:"Description"`
	File        string `json:"File"`
	StartLine   int    `json:"StartLine"`
	EndLine     int    `json:"EndLine"`
}

func (GitleaksEngine) Name() string { return "gitleaks" }

func (GitleaksEngine) Version(ctx context.Context) string {
	return scannerVersion(ctx, "gitleaks", "version")
}

func (GitleaksEngine) Scan(ctx context.Context, request ScanRequest) ([]Finding, error) {
	file, err := os.CreateTemp("", "codelocal-gitleaks-*.json")
	if err != nil {
		return nil, err
	}
	reportPath := file.Name()
	if err := file.Close(); err != nil {
		return nil, err
	}
	defer os.Remove(reportPath)

	_, err = runCommand(ctx, "gitleaks",
		"detect",
		"--source", request.Root,
		"--report-format", "json",
		"--report-path", reportPath,
		"--no-banner",
		"--redact",
		"--exit-code", "0",
	)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return parseGitleaksReport(raw)
}

func parseGitleaksReport(raw []byte) ([]Finding, error) {
	var results []gitleaksResult
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("decode gitleaks report: %w", err)
	}
	findings := make([]Finding, 0, len(results))
	for _, item := range results {
		title := item.Description
		if title == "" {
			title = item.RuleID
		}
		findings = append(findings, Finding{
			Engine:      "gitleaks",
			RuleID:      item.RuleID,
			Category:    "secret",
			Severity:    SeverityHigh,
			Title:       title,
			Description: "A credential-like value was detected. The matched value is intentionally omitted from the CodeLocal report.",
			File:        item.File,
			StartLine:   item.StartLine,
			EndLine:     item.EndLine,
			Evidence:    "[REDACTED]",
			Secret:      true,
		})
	}
	return findings, nil
}
