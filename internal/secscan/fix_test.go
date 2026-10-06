package secscan

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestBuildFixPlanIsAdvisoryAndPrioritizesDeterministicRemediation(t *testing.T) {
	report := Report{
		SchemaVersion: 1,
		ScanID:        "sec_fix",
		Findings: []Finding{
			{
				ID: "SEC-SECRET", Engine: "gitleaks", Category: "secret",
				Severity: SeverityHigh, Title: "secret", Secret: true,
				AI: &AIAnalysis{Status: "confirmed", RecommendedFix: "AI says something else"},
			},
			{
				ID: "SEC-DEP", Engine: "trivy", Category: "dependency-vulnerability",
				Severity: SeverityCritical, Title: "dependency",
				Metadata: map[string]string{"package": "example", "fixedVersion": "2.0.0"},
			},
			{
				ID: "SEC-CODE", Engine: "semgrep", Category: "source-code",
				Severity: SeverityMedium, Title: "code issue",
				AI: &AIAnalysis{Status: "confirmed", RecommendedFix: "Use a safe API."},
			},
		},
	}
	plan := BuildFixPlan(report)
	if plan.MutatesSource {
		t.Fatal("V1 fix plan must never mutate source")
	}
	if len(plan.Actions) != 3 {
		t.Fatalf("actions = %d", len(plan.Actions))
	}
	for _, action := range plan.Actions {
		if !action.ManualRequired {
			t.Fatalf("action unexpectedly automatic: %#v", action)
		}
	}
	if !strings.Contains(plan.Actions[0].Action, "Rotate or revoke") || plan.Actions[0].Source != "codelocal" {
		t.Fatalf("secret action = %#v", plan.Actions[0])
	}
	if !strings.Contains(plan.Actions[1].Action, "2.0.0") || plan.Actions[1].Source != "scanner" {
		t.Fatalf("dependency action = %#v", plan.Actions[1])
	}
	if plan.Actions[2].Action != "Use a safe API." || plan.Actions[2].Source != "ai" {
		t.Fatalf("source-code action = %#v", plan.Actions[2])
	}
}

func TestWriteFixPlanUsesPrivateFiles(t *testing.T) {
	root := t.TempDir()
	report := Report{
		SchemaVersion: 1,
		ScanID:        "sec_fix_private",
		StartedAt:     time.Now().UTC(),
		FinishedAt:    time.Now().UTC(),
		Findings: []Finding{{
			ID: "SEC-1", Engine: "semgrep", Category: "source-code",
			Severity: SeverityHigh, Title: "issue", Recommendation: "fix it",
		}},
	}
	written, err := WriteFixPlan(root, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{written.JSON, written.Markdown} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o", path, info.Mode().Perm())
		}
	}
	raw, err := os.ReadFile(written.Markdown)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Source files modified: **no**") {
		t.Fatalf("fix plan safety notice missing: %s", raw)
	}
}
