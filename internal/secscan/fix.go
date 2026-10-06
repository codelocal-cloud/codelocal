package secscan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type FixAction struct {
	FindingID      string   `json:"findingId"`
	Severity       Severity `json:"severity"`
	Engine         string   `json:"engine"`
	Category       string   `json:"category,omitempty"`
	File           string   `json:"file,omitempty"`
	StartLine      int      `json:"startLine,omitempty"`
	Title          string   `json:"title"`
	Action         string   `json:"action"`
	Source         string   `json:"source"`
	ManualRequired bool     `json:"manualRequired"`
}

type FixPlan struct {
	SchemaVersion int         `json:"schemaVersion"`
	ScanID        string      `json:"scanId"`
	GeneratedAt   time.Time   `json:"generatedAt"`
	MutatesSource bool        `json:"mutatesSource"`
	Actions       []FixAction `json:"actions"`
}

type WrittenFixPlan struct {
	JSON     string
	Markdown string
}

func BuildFixPlan(report Report) FixPlan {
	plan := FixPlan{
		SchemaVersion: 1,
		ScanID:        report.ScanID,
		GeneratedAt:   time.Now().UTC(),
		MutatesSource: false,
		Actions:       make([]FixAction, 0, len(report.Findings)),
	}
	for _, finding := range report.Findings {
		action, source := remediationForFinding(finding)
		plan.Actions = append(plan.Actions, FixAction{
			FindingID:      finding.ID,
			Severity:       finding.Severity,
			Engine:         finding.Engine,
			Category:       finding.Category,
			File:           finding.File,
			StartLine:      finding.StartLine,
			Title:          redactText(finding.Title),
			Action:         redactText(action),
			Source:         source,
			ManualRequired: true,
		})
	}
	return plan
}

func remediationForFinding(finding Finding) (string, string) {
	switch finding.Category {
	case "secret":
		return "Rotate or revoke the exposed credential first, remove it from source and relevant Git history, move the replacement into an approved secret store, then rescan.", "codelocal"
	case "dependency-vulnerability":
		if fixed := strings.TrimSpace(finding.Metadata["fixedVersion"]); fixed != "" {
			pkg := strings.TrimSpace(finding.Metadata["package"])
			if pkg == "" {
				pkg = "the affected dependency"
			}
			return fmt.Sprintf("Upgrade %s to %s or a later fixed version using the project's package manager, run the project's test/build checks, then rescan.", pkg, fixed), "scanner"
		}
	}
	if finding.AI != nil && strings.TrimSpace(finding.AI.RecommendedFix) != "" && finding.AI.Status != "not_actionable" {
		return finding.AI.RecommendedFix, "ai"
	}
	if strings.TrimSpace(finding.Recommendation) != "" {
		return finding.Recommendation, "scanner"
	}
	switch finding.Category {
	case "misconfiguration":
		return "Review the flagged configuration against the scanner rule, apply the minimum configuration change, validate the deployment configuration, then rescan.", "codelocal"
	case "source-code":
		return "Review the flagged code path and rule, apply the smallest defensive change that preserves behavior, run relevant tests, then rescan.", "codelocal"
	default:
		return "Review the finding, confirm impact in project context, apply the minimum defensive remediation, run relevant tests, then rescan.", "codelocal"
	}
}

func WriteFixPlan(root string, report Report) (WrittenFixPlan, error) {
	if !validReportScanID(report.ScanID) {
		return WrittenFixPlan{}, fmt.Errorf("invalid security scan ID %q", report.ScanID)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return WrittenFixPlan{}, err
	}
	projectRoot, err := os.OpenRoot(abs)
	if err != nil {
		return WrittenFixPlan{}, err
	}
	defer projectRoot.Close()

	reportDir := filepath.Join(".codelocal", "security", "reports")
	historyDir := filepath.Join(reportDir, "history")
	if err := ensureReportDirectories(projectRoot, reportDir, historyDir); err != nil {
		return WrittenFixPlan{}, err
	}

	plan := BuildFixPlan(report)
	jsonBytes, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return WrittenFixPlan{}, err
	}
	jsonRel := filepath.Join(reportDir, "fix-plan.json")
	markdownRel := filepath.Join(reportDir, "fix-plan.md")
	if err := writePrivate(projectRoot, jsonRel, jsonBytes); err != nil {
		return WrittenFixPlan{}, err
	}
	if err := writePrivate(projectRoot, markdownRel, []byte(renderFixPlanMarkdown(plan))); err != nil {
		return WrittenFixPlan{}, err
	}
	return WrittenFixPlan{
		JSON:     filepath.Join(abs, jsonRel),
		Markdown: filepath.Join(abs, markdownRel),
	}, nil
}

func renderFixPlanMarkdown(plan FixPlan) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# CodeLocal Security Remediation Plan")
	fmt.Fprintf(&b, "\n- Scan ID: %s\n- Generated: %s\n- Source files modified: **no**\n", plan.ScanID, plan.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintln(&b, "\n> This plan is advisory. CodeLocal does not execute commands or modify source files in V1 security --fix.")
	if len(plan.Actions) == 0 {
		fmt.Fprintln(&b, "\nNo remediation actions are required for the current report.")
		return b.String()
	}
	for _, action := range plan.Actions {
		fmt.Fprintf(&b, "\n## %s · %s · %s\n", action.FindingID, action.Severity, action.Title)
		fmt.Fprintf(&b, "\n- Engine: %s\n- Category: %s\n", action.Engine, action.Category)
		if action.File != "" {
			fmt.Fprintf(&b, "- Location: %s", action.File)
			if action.StartLine > 0 {
				fmt.Fprintf(&b, ":%d", action.StartLine)
			}
			fmt.Fprintln(&b)
		}
		fmt.Fprintf(&b, "- Recommendation source: %s\n- Manual review required: yes\n\n%s\n", action.Source, action.Action)
	}
	return b.String()
}
