package secscan

import (
	"sort"
	"time"
)

type Severity string

const (
	SeverityCritical Severity = "CRITICAL"
	SeverityHigh     Severity = "HIGH"
	SeverityMedium   Severity = "MEDIUM"
	SeverityLow      Severity = "LOW"
	SeverityUnknown  Severity = "UNKNOWN"
	StatusOpen                = "OPEN"
)

type Finding struct {
	ID             string            `json:"id"`
	Fingerprint    string            `json:"fingerprint"`
	Identity       string            `json:"-"`
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
	Evidence       string            `json:"evidence,omitempty"`
	Secret         bool              `json:"secret,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Status         string            `json:"status"`
	AI             *AIAnalysis       `json:"ai,omitempty"`
}

type EngineStatus struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	Findings int    `json:"findings"`
}

type Summary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
	Total    int `json:"total"`
}

type AIAnalysis struct {
	Status         string  `json:"status,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
	Exploitable    *bool   `json:"exploitable,omitempty"`
	Explanation    string  `json:"explanation,omitempty"`
	RecommendedFix string  `json:"recommendedFix,omitempty"`
	Priority       string  `json:"priority,omitempty"`
}

type AIState struct {
	Status     string     `json:"status"`
	Provider   string     `json:"provider,omitempty"`
	AnalyzedAt *time.Time `json:"analyzedAt,omitempty"`
}

type Comparison struct {
	PreviousScanID string   `json:"previousScanId,omitempty"`
	New            []string `json:"new"`
	Fixed          []string `json:"fixed"`
	Remaining      []string `json:"remaining"`
	Unassessed     []string `json:"unassessed"`
}

type Report struct {
	SchemaVersion      int            `json:"schemaVersion"`
	FingerprintVersion int            `json:"fingerprintVersion"`
	ScanID             string         `json:"scanId"`
	Project            string         `json:"project"`
	Root               string         `json:"-"`
	Branch             string         `json:"branch,omitempty"`
	Commit             string         `json:"commit,omitempty"`
	StartedAt          time.Time      `json:"startedAt"`
	FinishedAt         time.Time      `json:"finishedAt"`
	Offline            bool           `json:"offline"`
	Engines            []EngineStatus `json:"engines"`
	Summary            Summary        `json:"summary"`
	Findings           []Finding      `json:"findings"`
	Comparison         Comparison     `json:"comparison"`
	AI                 AIState        `json:"ai"`
}

func (report *Report) Recalculate() {
	var summary Summary
	for _, finding := range report.Findings {
		summary.Total++
		switch finding.Severity {
		case SeverityCritical:
			summary.Critical++
		case SeverityHigh:
			summary.High++
		case SeverityMedium:
			summary.Medium++
		case SeverityLow:
			summary.Low++
		default:
			summary.Unknown++
		}
	}
	report.Summary = summary
	order := map[Severity]int{
		SeverityCritical: 0,
		SeverityHigh:     1,
		SeverityMedium:   2,
		SeverityLow:      3,
		SeverityUnknown:  4,
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		if order[report.Findings[i].Severity] != order[report.Findings[j].Severity] {
			return order[report.Findings[i].Severity] < order[report.Findings[j].Severity]
		}
		if report.Findings[i].File != report.Findings[j].File {
			return report.Findings[i].File < report.Findings[j].File
		}
		return report.Findings[i].StartLine < report.Findings[j].StartLine
	})
}

type Options struct {
	Root    string
	Offline bool
	Engines []string
}
