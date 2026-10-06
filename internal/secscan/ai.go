package secscan

import (
	"strings"
	"time"
)

const MaxAIFindings = 50

type AIPayload struct {
	SchemaVersion int         `json:"schemaVersion"`
	ScanID        string      `json:"scanId"`
	Project       string      `json:"project"`
	Branch        string      `json:"branch,omitempty"`
	Commit        string      `json:"commit,omitempty"`
	Summary       Summary     `json:"summary"`
	Findings      []AIFinding `json:"findings"`
}

type AIFinding struct {
	ID             string            `json:"id"`
	Fingerprint    string            `json:"fingerprint"`
	Engine         string            `json:"engine"`
	RuleID         string            `json:"ruleId,omitempty"`
	Category       string            `json:"category,omitempty"`
	Severity       Severity          `json:"severity"`
	Title          string            `json:"title"`
	Description    string            `json:"description,omitempty"`
	Recommendation string            `json:"recommendation,omitempty"`
	File           string            `json:"file,omitempty"`
	StartLine      int               `json:"startLine,omitempty"`
	EndLine        int               `json:"endLine,omitempty"`
	CWE            []string          `json:"cwe,omitempty"`
	CVE            []string          `json:"cve,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type AIResult struct {
	ID             string  `json:"id"`
	Status         string  `json:"status,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
	Exploitable    *bool   `json:"exploitable,omitempty"`
	Explanation    string  `json:"explanation,omitempty"`
	RecommendedFix string  `json:"recommendedFix,omitempty"`
	Priority       string  `json:"priority,omitempty"`
}

type AIResponse struct {
	Provider string     `json:"provider,omitempty"`
	Results  []AIResult `json:"results"`
}

func BuildAIPayload(report Report) AIPayload {
	payload := AIPayload{
		SchemaVersion: report.SchemaVersion,
		ScanID:        report.ScanID,
		Findings:      make([]AIFinding, 0, min(len(report.Findings), MaxAIFindings)),
	}
	for _, raw := range report.Findings {
		if len(payload.Findings) == MaxAIFindings {
			break
		}
		finding := SanitizeFinding(raw)
		payload.Findings = append(payload.Findings, AIFinding{
			ID:          finding.ID,
			Fingerprint: finding.Fingerprint,
			Engine:      finding.Engine,
			RuleID:      finding.RuleID,
			Category:    finding.Category,
			Severity:    finding.Severity,
			StartLine:   finding.StartLine,
			CWE:         append([]string(nil), finding.CWE...),
			CVE:         append([]string(nil), finding.CVE...),
		})
	}
	return SanitizeAIPayload(payload)
}

func SanitizeAIPayload(payload AIPayload) AIPayload {
	payload.ScanID = redactText(payload.ScanID)
	payload.Project = redactText(payload.Project)
	payload.Branch = redactText(payload.Branch)
	payload.Commit = redactText(payload.Commit)
	for index := range payload.Findings {
		finding := &payload.Findings[index]
		finding.ID = redactText(finding.ID)
		finding.Fingerprint = redactText(finding.Fingerprint)
		finding.Engine = redactText(finding.Engine)
		finding.RuleID = redactText(finding.RuleID)
		finding.Category = redactText(finding.Category)
		finding.Severity = Severity(redactText(string(finding.Severity)))
		finding.Title = redactText(finding.Title)
		finding.Description = redactText(finding.Description)
		finding.Recommendation = redactText(finding.Recommendation)
		finding.File = redactText(finding.File)
		finding.CWE = redactStrings(finding.CWE)
		finding.CVE = redactStrings(finding.CVE)
		metadata := make(map[string]string, len(finding.Metadata))
		for key, value := range finding.Metadata {
			if sensitiveMetadataKey(key) {
				continue
			}
			metadata[redactText(key)] = redactText(value)
		}
		if len(metadata) == 0 {
			finding.Metadata = nil
		} else {
			finding.Metadata = metadata
		}
	}
	return payload
}

func redactStrings(values []string) []string {
	for index := range values {
		values[index] = redactText(values[index])
	}
	return values
}

func sensitiveMetadataKey(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "password") ||
		strings.Contains(lower, "credential") ||
		strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "private_key") ||
		strings.Contains(lower, "evidence") ||
		strings.Contains(lower, "snippet") ||
		strings.Contains(lower, "source") ||
		strings.Contains(lower, "raw")
}

func ApplyAIResponse(report *Report, response AIResponse, analyzedAt time.Time) {
	byID := make(map[string]AIResult, len(response.Results))
	for _, result := range response.Results {
		byID[result.ID] = result
	}
	for index := range report.Findings {
		result, ok := byID[report.Findings[index].ID]
		if !ok {
			continue
		}
		report.Findings[index].AI = &AIAnalysis{
			Status:         result.Status,
			Confidence:     result.Confidence,
			Exploitable:    result.Exploitable,
			Explanation:    redactText(result.Explanation),
			RecommendedFix: redactText(result.RecommendedFix),
			Priority:       result.Priority,
		}
	}
	analyzedAt = analyzedAt.UTC()
	report.AI = AIState{
		Status:     "complete",
		Provider:   response.Provider,
		AnalyzedAt: &analyzedAt,
	}
}
