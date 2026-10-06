package secscan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	maxPreviousReportBytes = 8 << 20
	maxHistoryReportBytes  = 32 << 20
	maxHistoryReports      = 128
)

func CompareWithLatest(root string, current *Report) error {
	if current == nil {
		return errors.New("current security report is required")
	}
	previous, err := readPreviousReports(root)
	if err != nil {
		return err
	}
	if len(previous) == 0 {
		CompareReports(current, nil)
		return nil
	}
	compareWithHistory(current, previous)
	return nil
}

func readPreviousReports(root string) ([]Report, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	projectRoot, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	defer projectRoot.Close()

	const reportsDir = securityReportsDirectory
	var reports []Report
	seen := map[string]struct{}{}
	var historyBytes int64
	appendReport := func(path string, required bool) error {
		info, err := projectRoot.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			if required {
				return fmt.Errorf("inspect previous security report: %w", err)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			if required {
				return fmt.Errorf("previous security report is not a regular file: %s", path)
			}
			return nil
		}
		if info.Size() > maxPreviousReportBytes ||
			(!required && historyBytes+info.Size() > maxHistoryReportBytes) {
			if required {
				return fmt.Errorf("previous security report is too large: %s", path)
			}
			return nil
		}
		if !required {
			historyBytes += info.Size()
		}
		file, err := projectRoot.Open(path)
		if err != nil {
			if required {
				return fmt.Errorf("read previous security report: %w", err)
			}
			return nil
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, maxPreviousReportBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			if required {
				return fmt.Errorf("read previous security report: %w", readErr)
			}
			return nil
		}
		if closeErr != nil {
			if required {
				return closeErr
			}
			return nil
		}
		if len(raw) > maxPreviousReportBytes {
			if required {
				return fmt.Errorf("previous security report is too large: %s", path)
			}
			return nil
		}
		var report Report
		if err := json.Unmarshal(raw, &report); err != nil {
			if required {
				return fmt.Errorf("decode previous security report: %w", err)
			}
			return nil
		}
		if _, duplicate := seen[report.ScanID]; duplicate {
			return nil
		}
		seen[report.ScanID] = struct{}{}
		reports = append(reports, report)
		return nil
	}
	for _, engine := range []string{"gitleaks", "trivy", "semgrep"} {
		path, _ := baselineReportRelativePath(engine)
		if err := appendReport(path, true); err != nil {
			return nil, err
		}
	}
	if err := appendReport(filepath.Join(reportsDir, "latest.json"), true); err != nil {
		return nil, err
	}

	historyPath := filepath.Join(reportsDir, "history")
	history, err := projectRoot.Open(historyPath)
	if errors.Is(err, os.ErrNotExist) {
		return reports, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open security report history: %w", err)
	}
	entries, readErr := history.ReadDir(maxHistoryReports)
	closeErr := history.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("read security report history: %w", readErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if err := appendReport(filepath.Join(historyPath, entry.Name()), false); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(reports, func(i, j int) bool {
		if reports[i].FinishedAt.Equal(reports[j].FinishedAt) {
			return reports[i].ScanID > reports[j].ScanID
		}
		return reports[i].FinishedAt.After(reports[j].FinishedAt)
	})
	return reports, nil
}

func compareWithHistory(current *Report, previous []Report) {
	comparison := Comparison{
		New:        []string{},
		Fixed:      []string{},
		Remaining:  []string{},
		Unassessed: []string{},
	}
	if len(previous) > 0 {
		comparison.PreviousScanID = previous[0].ScanID
	}

	currentStatus := make(map[string]string, len(current.Engines))
	engines := make(map[string]struct{}, len(current.Engines))
	for _, engine := range current.Engines {
		currentStatus[engine.Name] = engine.Status
		engines[engine.Name] = struct{}{}
	}
	for _, report := range previous {
		for _, engine := range report.Engines {
			engines[engine.Name] = struct{}{}
		}
	}

	currentByEngine := map[string][]Finding{}
	for _, finding := range current.Findings {
		currentByEngine[finding.Engine] = append(currentByEngine[finding.Engine], finding)
		engines[finding.Engine] = struct{}{}
	}

	for engine := range engines {
		var baseline *Report
		for index := range previous {
			if engineStatus(previous[index], engine) == "ok" {
				baseline = &previous[index]
				break
			}
		}
		var baselineFindings []Finding
		if baseline != nil {
			for _, finding := range baseline.Findings {
				if finding.Engine == engine {
					baselineFindings = append(baselineFindings, finding)
				}
			}
		}
		migrateFingerprint := baseline != nil &&
			baseline.FingerprintVersion < current.FingerprintVersion
		previousFindings := comparisonFindingMap(baselineFindings, migrateFingerprint)
		currentFindings := comparisonFindingMap(currentByEngine[engine], migrateFingerprint)
		if currentStatus[engine] != "ok" {
			for _, finding := range previousFindings {
				comparison.Unassessed = append(comparison.Unassessed, finding.ID)
			}
			continue
		}
		for key, finding := range currentFindings {
			if _, ok := previousFindings[key]; ok {
				comparison.Remaining = append(comparison.Remaining, finding.ID)
			} else {
				comparison.New = append(comparison.New, finding.ID)
			}
		}
		for key, finding := range previousFindings {
			if _, ok := currentFindings[key]; !ok {
				comparison.Fixed = append(comparison.Fixed, finding.ID)
			}
		}
	}
	sortComparison(&comparison)
	current.Comparison = comparison
}

func engineStatus(report Report, name string) string {
	for _, engine := range report.Engines {
		if engine.Name == name {
			return engine.Status
		}
	}
	return ""
}

func CompareReports(current *Report, previous *Report) {
	comparison := Comparison{
		New:        []string{},
		Fixed:      []string{},
		Remaining:  []string{},
		Unassessed: []string{},
	}
	previousByKey := map[string]Finding{}
	if previous != nil {
		comparison.PreviousScanID = previous.ScanID
		previousByKey = comparisonFindingMap(
			previous.Findings,
			previous.FingerprintVersion < current.FingerprintVersion,
		)
	}

	currentByKey := comparisonFindingMap(
		current.Findings,
		previous != nil && previous.FingerprintVersion < current.FingerprintVersion,
	)
	comparableEngines := make(map[string]struct{}, len(current.Engines))
	for _, engine := range current.Engines {
		if engine.Status == "ok" {
			comparableEngines[engine.Name] = struct{}{}
		}
	}
	for key, finding := range currentByKey {
		if _, ok := previousByKey[key]; ok {
			comparison.Remaining = append(comparison.Remaining, finding.ID)
		} else {
			comparison.New = append(comparison.New, finding.ID)
		}
	}
	for key, finding := range previousByKey {
		if _, ok := currentByKey[key]; !ok {
			if len(current.Engines) == 0 {
				comparison.Fixed = append(comparison.Fixed, finding.ID)
			} else if _, ok := comparableEngines[finding.Engine]; ok {
				comparison.Fixed = append(comparison.Fixed, finding.ID)
			} else {
				comparison.Unassessed = append(comparison.Unassessed, finding.ID)
			}
		}
	}
	sortComparison(&comparison)
	current.Comparison = comparison
}

func comparisonFindingMap(findings []Finding, migrate bool) map[string]Finding {
	result := make(map[string]Finding, len(findings))
	occurrences := map[string]int{}
	for _, finding := range findings {
		key := findingKey(finding)
		if migrate {
			identityKey := strings.Join([]string{
				finding.Engine,
				finding.RuleID,
				finding.Category,
				finding.File,
			}, "\x00")
			identity := finding.Identity
			if identity == "" {
				identity = finding.Metadata["package"]
			}
			if identity == "" {
				identity = fmt.Sprint(occurrences[identityKey])
				occurrences[identityKey]++
			}
			key = identityKey + "\x00" + identity
		}
		result[key] = finding
	}
	return result
}

func sortComparison(comparison *Comparison) {
	sort.Strings(comparison.New)
	sort.Strings(comparison.Fixed)
	sort.Strings(comparison.Remaining)
	sort.Strings(comparison.Unassessed)
}

func findingKey(finding Finding) string {
	if finding.Fingerprint != "" {
		return finding.Fingerprint
	}
	return finding.ID
}
