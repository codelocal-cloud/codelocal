package secscan

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

func NormalizeSeverity(value string) Severity {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CRITICAL":
		return SeverityCritical
	case "HIGH", "ERROR":
		return SeverityHigh
	case "MEDIUM", "WARNING", "WARN":
		return SeverityMedium
	case "LOW", "INFO":
		return SeverityLow
	default:
		return SeverityUnknown
	}
}

func finalizeFinding(root string, finding Finding) Finding {
	finding = SanitizeFinding(finding)
	finding.File = relativeFindingPath(root, finding.File)
	if finding.Severity == "" {
		finding.Severity = SeverityUnknown
	}
	if strings.TrimSpace(finding.Title) == "" {
		finding.Title = firstNonEmpty(finding.RuleID, finding.Description, "Security finding")
	}
	if finding.Status == "" {
		finding.Status = StatusOpen
	}
	parts := []string{
		finding.Engine,
		finding.RuleID,
		finding.Category,
		finding.File,
		finding.Identity,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	finding.Fingerprint = hex.EncodeToString(sum[:])
	finding.ID = "SEC-" + strings.ToUpper(finding.Fingerprint[:10])
	return finding
}

func relativeFindingPath(root, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		if rel, err := filepath.Rel(root, value); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
