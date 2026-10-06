package secscan

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type WrittenReports struct {
	Directory string
	JSON      string
	Markdown  string
	SARIF     string
	History   string
}

const securityReportsDirectory = ".codelocal/security/reports"

func WriteReports(root string, report Report) (WrittenReports, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return WrittenReports{}, err
	}
	if !validReportScanID(report.ScanID) {
		return WrittenReports{}, fmt.Errorf("invalid security scan ID %q", report.ScanID)
	}
	projectRoot, err := os.OpenRoot(abs)
	if err != nil {
		return WrittenReports{}, err
	}
	defer projectRoot.Close()

	dirRel := securityReportsDirectory
	historyDirRel := filepath.Join(dirRel, "history")
	baselinesDirRel := filepath.Join(dirRel, "baselines")
	if err := ensureReportDirectories(projectRoot, dirRel, historyDirRel, baselinesDirRel); err != nil {
		return WrittenReports{}, err
	}
	jsonRel := filepath.Join(dirRel, "latest.json")
	markdownRel := filepath.Join(dirRel, "latest.md")
	sarifRel := filepath.Join(dirRel, "latest.sarif")
	historyRel := filepath.Join(historyDirRel, report.ScanID+".json")

	jsonBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return WrittenReports{}, err
	}
	if err := writePrivate(projectRoot, jsonRel, jsonBytes); err != nil {
		return WrittenReports{}, err
	}
	if err := writePrivate(projectRoot, historyRel, jsonBytes); err != nil {
		return WrittenReports{}, err
	}
	if err := writePrivate(projectRoot, markdownRel, []byte(renderMarkdown(report))); err != nil {
		return WrittenReports{}, err
	}
	sarifBytes, err := json.MarshalIndent(renderSARIF(report), "", "  ")
	if err != nil {
		return WrittenReports{}, err
	}
	if err := writePrivate(projectRoot, sarifRel, sarifBytes); err != nil {
		return WrittenReports{}, err
	}
	for _, engine := range report.Engines {
		baselineRel, ok := baselineReportRelativePath(engine.Name)
		if engine.Status != "ok" || !ok {
			continue
		}
		if err := writePrivate(projectRoot, baselineRel, jsonBytes); err != nil {
			return WrittenReports{}, err
		}
	}
	dir := filepath.Join(abs, dirRel)
	return WrittenReports{
		Directory: dir,
		JSON:      filepath.Join(abs, jsonRel),
		Markdown:  filepath.Join(abs, markdownRel),
		SARIF:     filepath.Join(abs, sarifRel),
		History:   filepath.Join(abs, historyRel),
	}, nil
}

func ensureReportDirectories(root *os.Root, reportDir string, childDirs ...string) error {
	paths := []string{
		".codelocal",
		filepath.Join(".codelocal", "security"),
		reportDir,
	}
	paths = append(paths, childDirs...)
	privateDirs := map[string]struct{}{reportDir: {}}
	for _, path := range childDirs {
		privateDirs[path] = struct{}{}
	}
	for _, path := range paths {
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(path, 0o700); err != nil {
				return err
			}
			info, err = root.Lstat(path)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("security report path is not a directory: %s", path)
		}
		if _, ok := privateDirs[path]; !ok {
			continue
		}
		dir, err := root.Open(path)
		if err != nil {
			return err
		}
		chmodErr := dir.Chmod(0o700)
		closeErr := dir.Close()
		if chmodErr != nil {
			return chmodErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func baselineReportRelativePath(engine string) (string, bool) {
	switch engine {
	case "gitleaks", "trivy", "semgrep":
		return filepath.Join(securityReportsDirectory, "baselines", engine+".json"), true
	default:
		return "", false
	}
}

func writePrivate(root *os.Root, path string, data []byte) (err error) {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tempPath := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.tmp-%x", filepath.Base(path), suffix))
	file, err := root.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		root.Remove(tempPath)
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(tempPath, path)
}

func validReportScanID(value string) bool {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func renderMarkdown(report Report) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# CodeLocal Security Report")
	fmt.Fprintf(&b, "\n- Project: %s\n- Scan ID: %s\n- Branch: %s\n- Commit: %s\n- Started: %s\n- Finished: %s\n- Offline: %t\n- AI: %s\n",
		report.Project,
		report.ScanID,
		report.Branch,
		report.Commit,
		report.StartedAt.Format("2006-01-02 15:04:05Z"),
		report.FinishedAt.Format("2006-01-02 15:04:05Z"),
		report.Offline,
		report.AI.Status,
	)
	fmt.Fprintf(&b, "\n## Summary\n\nCritical: **%d** · High: **%d** · Medium: **%d** · Low: **%d** · Unknown: **%d** · Total: **%d**\n",
		report.Summary.Critical,
		report.Summary.High,
		report.Summary.Medium,
		report.Summary.Low,
		report.Summary.Unknown,
		report.Summary.Total,
	)
	fmt.Fprintf(&b, "\n## Comparison\n\nNew: **%d** · Fixed: **%d** · Remaining: **%d** · Unassessed: **%d**\n",
		len(report.Comparison.New),
		len(report.Comparison.Fixed),
		len(report.Comparison.Remaining),
		len(report.Comparison.Unassessed),
	)
	if report.Comparison.PreviousScanID != "" {
		fmt.Fprintf(&b, "\nPrevious scan: %s\n", report.Comparison.PreviousScanID)
	}
	writeFindingIDs(&b, "New", report.Comparison.New)
	writeFindingIDs(&b, "Fixed", report.Comparison.Fixed)
	writeFindingIDs(&b, "Remaining", report.Comparison.Remaining)
	writeFindingIDs(&b, "Unassessed", report.Comparison.Unassessed)

	fmt.Fprintln(&b, "\n## Engines")
	for _, engine := range report.Engines {
		fmt.Fprintf(&b, "\n- %s: %s", engine.Name, engine.Status)
		if engine.Version != "" {
			fmt.Fprintf(&b, " (%s)", engine.Version)
		}
		if engine.Findings > 0 {
			fmt.Fprintf(&b, " — %d findings", engine.Findings)
		}
		if engine.Error != "" {
			fmt.Fprintf(&b, " — %s", redactText(engine.Error))
		}
	}

	fmt.Fprintln(&b, "\n\n## Findings")
	if len(report.Findings) == 0 {
		fmt.Fprintln(&b, "\nNo findings.")
		return b.String()
	}
	for _, finding := range report.Findings {
		fmt.Fprintf(&b, "\n### %s · %s · %s\n\n", finding.ID, finding.Severity, finding.Title)
		fmt.Fprintf(&b, "- Engine: %s\n- Rule: %s\n- Category: %s\n- Location: %s", finding.Engine, finding.RuleID, finding.Category, finding.File)
		if finding.StartLine > 0 {
			fmt.Fprintf(&b, ":%d", finding.StartLine)
		}
		fmt.Fprintf(&b, "\n- Status: %s\n", finding.Status)
		if finding.Description != "" {
			fmt.Fprintf(&b, "\n%s\n", finding.Description)
		}
		if finding.Recommendation != "" {
			fmt.Fprintf(&b, "\n**Recommendation:** %s\n", finding.Recommendation)
		}
		if finding.AI != nil {
			fmt.Fprintf(&b, "\n**AI:** %s", finding.AI.Status)
			if finding.AI.Explanation != "" {
				fmt.Fprintf(&b, " — %s", finding.AI.Explanation)
			}
			fmt.Fprintln(&b)
		}
	}
	return b.String()
}

func renderSARIF(report Report) map[string]any {
	results := make([]map[string]any, 0, len(report.Findings))
	newFindings := stringSet(report.Comparison.New)
	remainingFindings := stringSet(report.Comparison.Remaining)
	for _, finding := range report.Findings {
		level := "note"
		switch finding.Severity {
		case SeverityCritical, SeverityHigh:
			level = "error"
		case SeverityMedium:
			level = "warning"
		}
		properties := map[string]any{
			"codelocalFindingId": finding.ID,
			"engine":             finding.Engine,
			"category":           finding.Category,
		}
		if finding.AI != nil {
			properties["aiStatus"] = finding.AI.Status
			properties["aiConfidence"] = finding.AI.Confidence
			properties["aiExploitable"] = finding.AI.Exploitable
			properties["aiExplanation"] = finding.AI.Explanation
			properties["aiRecommendedFix"] = finding.AI.RecommendedFix
			properties["aiPriority"] = finding.AI.Priority
		}
		result := map[string]any{
			"ruleId":     finding.RuleID,
			"level":      level,
			"message":    map[string]any{"text": firstNonEmpty(finding.Description, finding.Title)},
			"properties": properties,
		}
		if _, ok := newFindings[finding.ID]; ok {
			result["baselineState"] = "new"
		} else if _, ok := remainingFindings[finding.ID]; ok {
			result["baselineState"] = "unchanged"
		}
		if finding.File != "" {
			physical := map[string]any{
				"artifactLocation": map[string]any{"uri": finding.File},
			}
			if finding.StartLine > 0 {
				physical["region"] = map[string]any{"startLine": finding.StartLine}
			}
			result["locations"] = []any{map[string]any{"physicalLocation": physical}}
		}
		results = append(results, result)
	}
	return map[string]any{
		"version": "2.1.0",
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"runs": []any{
			map[string]any{
				"tool": map[string]any{"driver": map[string]any{"name": "CodeLocal Security"}},
				"properties": map[string]any{
					"previousScanId": report.Comparison.PreviousScanID,
					"new":            report.Comparison.New,
					"fixed":          report.Comparison.Fixed,
					"remaining":      report.Comparison.Remaining,
					"unassessed":     report.Comparison.Unassessed,
				},
				"results": results,
			},
		},
	}
}

func writeFindingIDs(b *strings.Builder, label string, ids []string) {
	if len(ids) > 0 {
		fmt.Fprintf(b, "\n- %s: %s\n", label, strings.Join(ids, ", "))
	}
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}
