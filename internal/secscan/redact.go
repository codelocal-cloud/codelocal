package secscan

import (
	"regexp"
	"strings"
)

var sensitiveValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|auth[_-]?token|token|secret|client[_-]?secret|password|passwd|credential|private[_-]?key)\s*[:=]\s*([^\s,;]+)`),
	regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)(sk|ghp|github_pat|xox[baprs])-[-a-z0-9_]{8,}`),
}

func SanitizeFinding(finding Finding) Finding {
	finding.Title = redactText(finding.Title)
	finding.Description = redactText(finding.Description)
	finding.Recommendation = redactText(finding.Recommendation)
	if finding.Secret {
		finding.Evidence = "[REDACTED]"
	} else {
		finding.Evidence = redactText(finding.Evidence)
	}
	for key, value := range finding.Metadata {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") {
			finding.Metadata[key] = "[REDACTED]"
			continue
		}
		finding.Metadata[key] = redactText(value)
	}
	return finding
}

func redactText(value string) string {
	result := value
	for index, pattern := range sensitiveValuePatterns {
		if index == 0 {
			result = pattern.ReplaceAllString(result, "$1=[REDACTED]")
			continue
		}
		result = pattern.ReplaceAllString(result, "[REDACTED]")
	}
	return result
}
