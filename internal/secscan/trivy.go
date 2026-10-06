package secscan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type TrivyEngine struct{}

type trivyReport struct {
	Results []struct {
		Target          string `json:"Target"`
		Vulnerabilities []struct {
			VulnerabilityID  string `json:"VulnerabilityID"`
			PkgName          string `json:"PkgName"`
			InstalledVersion string `json:"InstalledVersion"`
			FixedVersion     string `json:"FixedVersion"`
			Severity         string `json:"Severity"`
			Title            string `json:"Title"`
			Description      string `json:"Description"`
		} `json:"Vulnerabilities"`
		Misconfigurations []struct {
			ID            string `json:"ID"`
			Title         string `json:"Title"`
			Description   string `json:"Description"`
			Message       string `json:"Message"`
			Resolution    string `json:"Resolution"`
			Severity      string `json:"Severity"`
			CauseMetadata struct {
				StartLine int `json:"StartLine"`
				EndLine   int `json:"EndLine"`
			} `json:"CauseMetadata"`
		} `json:"Misconfigurations"`
	} `json:"Results"`
}

func (TrivyEngine) Name() string { return "trivy" }

func (TrivyEngine) Version(ctx context.Context) string {
	return scannerVersion(ctx, "trivy", "--version")
}

func (TrivyEngine) Scan(ctx context.Context, request ScanRequest) ([]Finding, error) {
	args := []string{
		"fs",
		"--format", "json",
		"--scanners", "vuln,misconfig",
		"--quiet",
	}
	if request.Offline {
		args = append(args, "--offline-scan", "--skip-db-update", "--skip-check-update")
	}
	args = append(args, request.Root)
	result, err := runCommand(ctx, "trivy", args...)
	if err != nil {
		return nil, err
	}
	return parseTrivyReport(result.Stdout)
}

func parseTrivyReport(raw []byte) ([]Finding, error) {
	var report trivyReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode trivy report: %w", err)
	}

	var findings []Finding
	for _, target := range report.Results {
		for _, vulnerability := range target.Vulnerabilities {
			title := strings.TrimSpace(vulnerability.Title)
			if title == "" {
				title = vulnerability.VulnerabilityID
			}
			recommendation := ""
			if vulnerability.FixedVersion != "" {
				recommendation = fmt.Sprintf("Upgrade %s from %s to %s or a later fixed version.", vulnerability.PkgName, vulnerability.InstalledVersion, vulnerability.FixedVersion)
			}
			cves := []string{}
			if strings.HasPrefix(strings.ToUpper(vulnerability.VulnerabilityID), "CVE-") {
				cves = append(cves, vulnerability.VulnerabilityID)
			}
			findings = append(findings, Finding{
				Engine:         "trivy",
				RuleID:         vulnerability.VulnerabilityID,
				Category:       "dependency-vulnerability",
				Identity:       vulnerability.PkgName,
				Severity:       NormalizeSeverity(vulnerability.Severity),
				Title:          title,
				Description:    vulnerability.Description,
				Recommendation: recommendation,
				File:           target.Target,
				CVE:            cves,
				Metadata: map[string]string{
					"package":          vulnerability.PkgName,
					"installedVersion": vulnerability.InstalledVersion,
					"fixedVersion":     vulnerability.FixedVersion,
				},
			})
		}
		for _, misconfiguration := range target.Misconfigurations {
			description := strings.TrimSpace(misconfiguration.Description)
			if description == "" {
				description = strings.TrimSpace(misconfiguration.Message)
			}
			findings = append(findings, Finding{
				Engine:         "trivy",
				RuleID:         misconfiguration.ID,
				Category:       "misconfiguration",
				Severity:       NormalizeSeverity(misconfiguration.Severity),
				Title:          misconfiguration.Title,
				Description:    description,
				Recommendation: misconfiguration.Resolution,
				File:           target.Target,
				StartLine:      misconfiguration.CauseMetadata.StartLine,
				EndLine:        misconfiguration.CauseMetadata.EndLine,
			})
		}
	}
	return findings, nil
}
