package secscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFinalizeFindingRedactsSecrets(t *testing.T) {
	root := t.TempDir()
	finding := finalizeFinding(root, Finding{
		Engine:      "semgrep",
		RuleID:      "test",
		Severity:    SeverityHigh,
		File:        filepath.Join(root, "src", "x.go"),
		StartLine:   12,
		Title:       "token test",
		Description: "api_key=sk-abcdefghijklmnopqrstuvwxyz",
	})
	if strings.Contains(finding.Description, "sk-") {
		t.Fatalf("secret was not redacted: %q", finding.Description)
	}
	if finding.File != "src/x.go" {
		t.Fatalf("file = %q", finding.File)
	}
	if finding.ID == "" || finding.Fingerprint == "" {
		t.Fatal("finding identity not generated")
	}
}

func TestBuildAIPayloadOmitsEvidenceAndSensitiveMetadata(t *testing.T) {
	report := Report{
		SchemaVersion: 1,
		ScanID:        "sec_test",
		Project:       "fixture",
		Findings: []Finding{{
			ID:          "SEC-1",
			Fingerprint: "abc",
			Engine:      "gitleaks",
			RuleID:      "generic-secret",
			Category:    "secret",
			Severity:    SeverityHigh,
			Title:       "Secret found",
			Description: "token=[REDACTED]",
			File:        ".env",
			StartLine:   1,
			Evidence:    "must-never-leave",
			Secret:      true,
			Metadata:    map[string]string{"token": "must-never-leave", "safe": "ok"},
		}},
	}
	report.Recalculate()
	payload := BuildAIPayload(report)
	if len(payload.Findings) != 1 {
		t.Fatalf("AI findings = %d", len(payload.Findings))
	}
	if payload.Findings[0].Metadata["token"] != "" {
		t.Fatal("sensitive metadata reached AI payload")
	}
	if payload.Findings[0].Metadata != nil {
		t.Fatalf("gitleaks metadata reached AI payload: %v", payload.Findings[0].Metadata)
	}
}

func TestBuildAIPayloadOmitsScannerFreeText(t *testing.T) {
	report := Report{
		SchemaVersion: 1,
		ScanID:        "sec_20260925T120000.000000000Z",
		Project:       "project-must-stay-local",
		Branch:        "branch-must-stay-local",
		Commit:        "commit-must-stay-local",
		Findings: []Finding{{
			ID:             "SEC-1234567890",
			Fingerprint:    strings.Repeat("a", 64),
			Engine:         "semgrep",
			RuleID:         "go.security.test",
			Category:       "source-code",
			Severity:       SeverityHigh,
			Title:          "source-value-from-metavariable",
			Description:    "credential=must-never-reach-model",
			Recommendation: "fix source-value-from-metavariable",
			File:           "path-must-stay-local/main.go",
			Metadata:       map[string]string{"package": "package-must-stay-local"},
		}},
	}
	payload := BuildAIPayload(report)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"source-value-from-metavariable",
		"must-never-reach-model",
		"project-must-stay-local",
		"branch-must-stay-local",
		"commit-must-stay-local",
		"path-must-stay-local",
		"package-must-stay-local",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("%q reached AI payload: %s", forbidden, raw)
		}
	}
}

func TestBuildAIPayloadCapsFindings(t *testing.T) {
	if MaxAIFindings != 50 {
		t.Fatalf("MaxAIFindings = %d, want 50", MaxAIFindings)
	}
	report := Report{Findings: make([]Finding, MaxAIFindings+10)}
	for index := range report.Findings {
		report.Findings[index] = Finding{
			ID:          "SEC-1234567890",
			Fingerprint: strings.Repeat("a", 64),
			Engine:      "semgrep",
			RuleID:      "go.security.test",
			Category:    "source-code",
			Severity:    SeverityHigh,
		}
	}
	if got := len(BuildAIPayload(report).Findings); got != MaxAIFindings {
		t.Fatalf("AI findings = %d, want %d", got, MaxAIFindings)
	}
}

func TestSanitizeAIPayloadRedactsUntrustedText(t *testing.T) {
	payload := SanitizeAIPayload(AIPayload{
		ScanID:  "token=scan-secret",
		Project: "token=project-secret",
		Findings: []AIFinding{{
			ID:             "token=id-secret",
			Fingerprint:    "token=fingerprint-secret",
			Engine:         "token=engine-secret",
			RuleID:         "token=rule-secret",
			Category:       "token=category-secret",
			Severity:       Severity("token=severity-secret"),
			Title:          "token=title-secret",
			Description:    "password=hunter2",
			Recommendation: "token=recommendation-secret",
			File:           "token=file-secret",
			CWE:            []string{"token=cwe-secret"},
			CVE:            []string{"token=cve-secret"},
			Metadata: map[string]string{
				"credentialValue": "must-never-leave",
				"sourceSnippet":   "raw-source-must-never-leave",
				"safe":            "api_key=still-secret",
			},
		}},
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"scan-secret", "project-secret", "id-secret", "fingerprint-secret",
		"engine-secret", "rule-secret", "category-secret", "severity-secret",
		"title-secret", "hunter2", "recommendation-secret", "file-secret",
		"cwe-secret", "cve-secret", "must-never-leave",
		"raw-source-must-never-leave", "still-secret",
	} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q reached sanitized payload: %s", secret, raw)
		}
	}
}

func TestRedactTextCoversCredentialAliases(t *testing.T) {
	got := redactText("credential=one private_key=two client-secret=three")
	for _, secret := range []string{"one", "two", "three"} {
		if strings.Contains(got, secret) {
			t.Fatalf("secret %q survived redaction: %q", secret, got)
		}
	}
}

func TestCompareReports(t *testing.T) {
	previous := Report{
		ScanID: "sec_previous",
		Findings: []Finding{
			{ID: "SEC-FIXED", Fingerprint: "fixed"},
			{ID: "SEC-REMAINING", Fingerprint: "remaining"},
		},
	}
	current := Report{
		ScanID: "sec_current",
		Findings: []Finding{
			{ID: "SEC-REMAINING", Fingerprint: "remaining"},
			{ID: "SEC-NEW", Fingerprint: "new"},
		},
	}
	CompareReports(&current, &previous)
	if current.Comparison.PreviousScanID != previous.ScanID {
		t.Fatalf("previous scan = %q", current.Comparison.PreviousScanID)
	}
	if strings.Join(current.Comparison.New, ",") != "SEC-NEW" {
		t.Fatalf("new = %v", current.Comparison.New)
	}
	if strings.Join(current.Comparison.Fixed, ",") != "SEC-FIXED" {
		t.Fatalf("fixed = %v", current.Comparison.Fixed)
	}
	if strings.Join(current.Comparison.Remaining, ",") != "SEC-REMAINING" {
		t.Fatalf("remaining = %v", current.Comparison.Remaining)
	}
}

func TestCompareReportsDoesNotFixFindingsFromUnavailableEngine(t *testing.T) {
	previous := Report{
		ScanID: "sec_previous",
		Findings: []Finding{{
			ID:          "SEC-OLD",
			Fingerprint: "old",
			Engine:      "semgrep",
		}},
	}
	current := Report{
		ScanID: "sec_current",
		Engines: []EngineStatus{{
			Name:   "semgrep",
			Status: "unavailable",
		}},
	}
	CompareReports(&current, &previous)
	if len(current.Comparison.Fixed) != 0 {
		t.Fatalf("fixed = %v", current.Comparison.Fixed)
	}
	if strings.Join(current.Comparison.Unassessed, ",") != "SEC-OLD" {
		t.Fatalf("unassessed = %v", current.Comparison.Unassessed)
	}
}

func TestCompareReportsMigratesLineBasedFingerprint(t *testing.T) {
	previous := Report{
		SchemaVersion:      1,
		FingerprintVersion: 0,
		ScanID:             "sec_previous",
		Findings: []Finding{{
			ID:          "SEC-OLD-ID",
			Fingerprint: "old-line-based-fingerprint",
			Engine:      "semgrep",
			RuleID:      "rule",
			Category:    "source-code",
			File:        "main.go",
			StartLine:   7,
		}},
	}
	currentFinding := finalizeFinding(t.TempDir(), Finding{
		Engine: "semgrep", RuleID: "rule", Category: "source-code", File: "main.go", StartLine: 70, Identity: "0",
	})
	current := Report{
		SchemaVersion:      1,
		FingerprintVersion: 2,
		ScanID:             "sec_current",
		Findings:           []Finding{currentFinding},
	}
	CompareReports(&current, &previous)
	if len(current.Comparison.New) != 0 || len(current.Comparison.Fixed) != 0 {
		t.Fatalf("migration comparison = new %v fixed %v", current.Comparison.New, current.Comparison.Fixed)
	}
	if strings.Join(current.Comparison.Remaining, ",") != currentFinding.ID {
		t.Fatalf("remaining = %v", current.Comparison.Remaining)
	}
}

func TestFindingFingerprintIgnoresScannerWording(t *testing.T) {
	root := t.TempDir()
	first := finalizeFinding(root, Finding{
		Engine: "semgrep", RuleID: "rule", File: "main.go", StartLine: 7, Title: "old wording",
	})
	second := finalizeFinding(root, Finding{
		Engine: "semgrep", RuleID: "rule", File: "main.go", StartLine: 7, Title: "new wording",
	})
	if first.Fingerprint != second.Fingerprint || first.ID != second.ID {
		t.Fatalf("finding identity changed: %s != %s", first.ID, second.ID)
	}
}

func TestFindingFingerprintIgnoresLineMovement(t *testing.T) {
	root := t.TempDir()
	first := finalizeFinding(root, Finding{
		Engine: "semgrep", RuleID: "rule", File: "main.go", StartLine: 7, Identity: "0",
	})
	second := finalizeFinding(root, Finding{
		Engine: "semgrep", RuleID: "rule", File: "main.go", StartLine: 70, Identity: "0",
	})
	if first.Fingerprint != second.Fingerprint || first.ID != second.ID {
		t.Fatalf("finding identity changed after line movement: %s != %s", first.ID, second.ID)
	}
}

func TestOrchestratorKeepsRepeatedRuleMatchesDistinct(t *testing.T) {
	root := t.TempDir()
	engine := staticTestEngine{
		name: "semgrep",
		findings: []Finding{
			{Engine: "semgrep", RuleID: "rule", Category: "source-code", File: "main.go", StartLine: 7},
			{Engine: "semgrep", RuleID: "rule", Category: "source-code", File: "main.go", StartLine: 70},
		},
	}
	report, err := (Orchestrator{Engines: []Engine{engine}, Now: time.Now}).Scan(context.Background(), ScanRequest{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 2 {
		t.Fatalf("findings = %d, want 2", len(report.Findings))
	}
	if report.Findings[0].Fingerprint == report.Findings[1].Fingerprint {
		t.Fatal("repeated rule matches were deduplicated")
	}
}

func TestTrivyDependencyFingerprintIncludesPackage(t *testing.T) {
	findings, err := parseTrivyReport([]byte(`{"Results":[{"Target":"go.mod","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-1","PkgName":"package-a","Severity":"HIGH"},{"VulnerabilityID":"CVE-2026-1","PkgName":"package-b","Severity":"HIGH"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	first := finalizeFinding(t.TempDir(), findings[0])
	second := finalizeFinding(t.TempDir(), findings[1])
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("different packages with the same CVE shared a fingerprint")
	}
}

func TestRunRejectsUnknownEngine(t *testing.T) {
	_, err := Run(context.Background(), Options{
		Root:    t.TempDir(),
		Offline: true,
		Engines: []string{"gitleaks", "made-up"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown security engine(s): made-up") {
		t.Fatalf("error = %v", err)
	}
}

func TestOfflineSemgrepRequiresReadableLocalConfig(t *testing.T) {
	t.Setenv("CODELOCAL_SEMGREP_CONFIG", "https://example.com/rules.yml")
	_, err := (SemgrepEngine{}).Scan(context.Background(), ScanRequest{
		Root:    t.TempDir(),
		Offline: true,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestScannerParsers(t *testing.T) {
	gitleaks, err := parseGitleaksReport([]byte(`[{"RuleID":"generic-api-key","Description":"secret","File":".env","StartLine":2,"EndLine":2}]`))
	if err != nil || len(gitleaks) != 1 || !gitleaks[0].Secret || gitleaks[0].Evidence != "[REDACTED]" {
		t.Fatalf("gitleaks = %#v, err = %v", gitleaks, err)
	}

	trivy, err := parseTrivyReport([]byte(`{"Results":[{"Target":"go.mod","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-1","PkgName":"example","InstalledVersion":"1","FixedVersion":"2","Severity":"CRITICAL","Title":"vuln"}],"Misconfigurations":[{"ID":"CFG-1","Title":"config","Severity":"MEDIUM","CauseMetadata":{"StartLine":7,"EndLine":8}}]}]}`))
	if err != nil || len(trivy) != 2 || trivy[0].Severity != SeverityCritical || trivy[1].StartLine != 7 {
		t.Fatalf("trivy = %#v, err = %v", trivy, err)
	}

	semgrep, err := parseSemgrepReport([]byte(`{"results":[{"check_id":"go.lang.security.test","path":"main.go","start":{"line":3},"end":{"line":4},"extra":{"message":"issue","severity":"ERROR","fix":"fix it"}}]}`))
	if err != nil || len(semgrep) != 1 || semgrep[0].Severity != SeverityHigh || semgrep[0].Evidence != "" {
		t.Fatalf("semgrep = %#v, err = %v", semgrep, err)
	}
}

func TestWriteReports(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	report := Report{
		SchemaVersion: 1,
		ScanID:        "sec_test",
		Project:       "fixture",
		StartedAt:     now,
		FinishedAt:    now,
		Engines:       []EngineStatus{{Name: "semgrep", Status: "ok", Findings: 1}},
		AI:            AIState{Status: "not-run"},
		Comparison:    Comparison{New: []string{"SEC-1"}, Fixed: []string{}, Remaining: []string{}, Unassessed: []string{}},
		Findings: []Finding{{
			ID:          "SEC-1",
			Fingerprint: "x",
			Engine:      "semgrep",
			RuleID:      "R1",
			Severity:    SeverityHigh,
			Title:       "test",
			File:        "main.go",
			StartLine:   1,
			Status:      StatusOpen,
			AI:          &AIAnalysis{Status: "confirmed", Confidence: 0.9},
		}},
	}
	report.Recalculate()
	written, err := WriteReports(root, report)
	if err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(written.Directory, "baselines", "semgrep.json")
	for _, path := range []string{written.JSON, written.Markdown, written.SARIF, written.History, baseline} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing report %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("report mode = %o, want 600: %s", info.Mode().Perm(), path)
		}
	}
	for _, path := range []string{written.Directory, filepath.Dir(written.History), filepath.Dir(baseline)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing report directory %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("report directory mode = %o, want 700: %s", info.Mode().Perm(), path)
		}
	}
	sarif, err := os.ReadFile(written.SARIF)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sarif), `"codelocalFindingId": "SEC-1"`) {
		t.Fatalf("SARIF missing finding identity: %s", sarif)
	}
	if !strings.Contains(string(sarif), `"baselineState": "new"`) ||
		!strings.Contains(string(sarif), `"aiStatus": "confirmed"`) {
		t.Fatalf("SARIF missing comparison or AI analysis: %s", sarif)
	}
}

func TestWriteReportsDoesNotFollowLatestSymlink(t *testing.T) {
	root := t.TempDir()
	reportDir := filepath.Join(root, ".codelocal", "security", "reports")
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "sentinel.json")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	latest := filepath.Join(reportDir, "latest.json")
	if err := os.Symlink(outside, latest); err != nil {
		t.Fatal(err)
	}

	report := testReport("sec_symlink", time.Now().UTC(), "semgrep", "ok", nil)
	if _, err := WriteReports(root, report); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "unchanged" {
		t.Fatalf("outside symlink target changed: %q", content)
	}
	info, err := os.Lstat(latest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("latest report remained a symlink")
	}
}

func TestWriteReportsRejectsEscapingDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".codelocal"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".codelocal", "security")); err != nil {
		t.Fatal(err)
	}
	report := testReport("sec_escape", time.Now().UTC(), "semgrep", "ok", nil)
	if _, err := WriteReports(root, report); err == nil {
		t.Fatal("escaping report directory symlink was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "reports")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside reports path exists or returned unexpected error: %v", err)
	}
}

func TestWriteReportsRejectsInternalDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".codelocal"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "target"), filepath.Join(root, ".codelocal", "security")); err != nil {
		t.Fatal(err)
	}
	report := testReport("sec_internal_link", time.Now().UTC(), "semgrep", "ok", nil)
	if _, err := WriteReports(root, report); err == nil {
		t.Fatal("internal report directory symlink was accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "reports")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("redirected reports path exists or returned unexpected error: %v", err)
	}
}

func TestWriteReportsRejectsUnsafeScanID(t *testing.T) {
	report := testReport("../../escape", time.Now().UTC(), "semgrep", "ok", nil)
	if _, err := WriteReports(t.TempDir(), report); err == nil {
		t.Fatal("unsafe scan ID was accepted")
	}
}

func TestCompareWithLatestUsesLastSuccessfulEngineBaseline(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-3 * time.Minute)
	finding := Finding{ID: "SEC-STABLE", Fingerprint: "stable", Engine: "semgrep"}

	first := testReport("sec_a", start, "semgrep", "ok", []Finding{finding})
	if _, err := WriteReports(root, first); err != nil {
		t.Fatal(err)
	}

	partial := testReport("sec_b", start.Add(time.Minute), "semgrep", "unavailable", nil)
	if err := CompareWithLatest(root, &partial); err != nil {
		t.Fatal(err)
	}
	if strings.Join(partial.Comparison.Unassessed, ",") != "SEC-STABLE" {
		t.Fatalf("partial unassessed = %v", partial.Comparison.Unassessed)
	}
	if _, err := WriteReports(root, partial); err != nil {
		t.Fatal(err)
	}

	recovered := testReport("sec_c", start.Add(2*time.Minute), "semgrep", "ok", []Finding{finding})
	if err := CompareWithLatest(root, &recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Comparison.New) != 0 {
		t.Fatalf("recovered new = %v", recovered.Comparison.New)
	}
	if strings.Join(recovered.Comparison.Remaining, ",") != "SEC-STABLE" {
		t.Fatalf("recovered remaining = %v", recovered.Comparison.Remaining)
	}
}

func TestCompareWithLatestRetainsEngineAcrossRepeatedSelectedScans(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-3 * time.Minute)
	finding := Finding{ID: "SEC-TRIVY", Fingerprint: "trivy-stable", Engine: "trivy"}
	first := testReport("sec_engine_a", start, "trivy", "ok", []Finding{finding})
	if _, err := WriteReports(root, first); err != nil {
		t.Fatal(err)
	}
	for index, scanID := range []string{"sec_engine_b", "sec_engine_c"} {
		current := testReport(scanID, start.Add(time.Duration(index+1)*time.Minute), "gitleaks", "ok", nil)
		if err := CompareWithLatest(root, &current); err != nil {
			t.Fatal(err)
		}
		if strings.Join(current.Comparison.Unassessed, ",") != "SEC-TRIVY" {
			t.Fatalf("%s unassessed = %v", scanID, current.Comparison.Unassessed)
		}
		if _, err := WriteReports(root, current); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompareWithLatestUsesBaselineBeyondHistoryLimit(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-3 * time.Hour)
	trivyFinding := Finding{ID: "SEC-TRIVY", Fingerprint: "trivy-stable", Engine: "trivy"}
	if _, err := WriteReports(root, testReport("sec_trivy", start, "trivy", "ok", []Finding{trivyFinding})); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxHistoryReports+2; index++ {
		report := testReport(
			fmt.Sprintf("sec_gitleaks_%03d", index),
			start.Add(time.Duration(index+1)*time.Minute),
			"gitleaks",
			"ok",
			nil,
		)
		if _, err := WriteReports(root, report); err != nil {
			t.Fatal(err)
		}
	}

	current := testReport("sec_current", start.Add(4*time.Hour), "gitleaks", "ok", nil)
	if err := CompareWithLatest(root, &current); err != nil {
		t.Fatal(err)
	}
	if strings.Join(current.Comparison.Unassessed, ",") != "SEC-TRIVY" {
		t.Fatalf("unassessed = %v", current.Comparison.Unassessed)
	}
}

func TestWriteReportsDoesNotOverwriteFailedEngineBaseline(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-time.Minute)
	finding := Finding{ID: "SEC-TRIVY", Fingerprint: "trivy-stable", Engine: "trivy"}
	if _, err := WriteReports(root, testReport("sec_ok", start, "trivy", "ok", []Finding{finding})); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(root, securityReportsDirectory, "baselines", "trivy.json")
	before, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteReports(root, testReport("sec_failed", start.Add(time.Minute), "trivy", "error", nil)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed engine overwrote successful baseline")
	}
}

func TestWriteReportsDoesNotFollowBaselineSymlink(t *testing.T) {
	root := t.TempDir()
	baselineDir := filepath.Join(root, securityReportsDirectory, "baselines")
	if err := os.MkdirAll(baselineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "sentinel.json")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(baselineDir, "trivy.json")
	if err := os.Symlink(outside, baseline); err != nil {
		t.Fatal(err)
	}
	report := testReport("sec_baseline_link", time.Now().UTC(), "trivy", "ok", nil)
	if _, err := WriteReports(root, report); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "unchanged" {
		t.Fatalf("outside baseline target changed: %q", content)
	}
	info, err := os.Lstat(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("baseline remained a symlink")
	}
}

func TestCompareWithLatestRejectsOversizedLatestReport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".codelocal", "security", "reports", "latest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxPreviousReportBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	current := testReport("sec_bounded", time.Now().UTC(), "semgrep", "ok", nil)
	if err := CompareWithLatest(root, &current); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v", err)
	}
}

type staticTestEngine struct {
	name     string
	findings []Finding
}

func (engine staticTestEngine) Name() string { return engine.name }
func (staticTestEngine) Version(context.Context) string {
	return "test"
}
func (engine staticTestEngine) Scan(context.Context, ScanRequest) ([]Finding, error) {
	return engine.findings, nil
}

func testReport(scanID string, finished time.Time, engineName, engineStatus string, findings []Finding) Report {
	report := Report{
		SchemaVersion:      1,
		FingerprintVersion: 2,
		ScanID:             scanID,
		Project:            "fixture",
		StartedAt:          finished.Add(-time.Second),
		FinishedAt:         finished,
		Engines:            []EngineStatus{{Name: engineName, Status: engineStatus}},
		Findings:           findings,
		Comparison:         Comparison{New: []string{}, Fixed: []string{}, Remaining: []string{}, Unassessed: []string{}},
		AI:                 AIState{Status: "not-run"},
	}
	report.Recalculate()
	return report
}
