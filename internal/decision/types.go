package decision

import "time"

type Kind string

const (
	KindBoolean Kind = "boolean"
	KindChoice  Kind = "choice"
	KindScore   Kind = "score"
	KindRank    Kind = "rank"
)

type Candidate struct {
	ID       string            `json:"id"`
	Label    string            `json:"label,omitempty"`
	Prior    float64           `json:"prior,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Meta struct {
	Provider   string        `json:"provider"`
	Confidence float64       `json:"confidence"`
	Latency    time.Duration `json:"-"`
	LatencyMS  int64         `json:"latencyMs"`
	Fallback   bool          `json:"fallback,omitempty"`
	ReasonCode string        `json:"reasonCode,omitempty"`
}

type BooleanRequest struct {
	Purpose string   `json:"purpose"`
	Input   string   `json:"input"`
	Prior   *float64 `json:"prior,omitempty"`
}

type BooleanResult struct {
	Value bool `json:"value"`
	Meta
}

type ChoiceRequest struct {
	Purpose    string      `json:"purpose"`
	Candidates []Candidate `json:"candidates"`
}

type ChoiceResult struct {
	ChoiceID string             `json:"choiceId"`
	Scores   map[string]float64 `json:"scores,omitempty"`
	Meta
}

type ScoreRequest struct {
	Purpose string    `json:"purpose"`
	Item    Candidate `json:"item"`
}

type ScoreResult struct {
	Score float64 `json:"score"`
	Meta
}

type RankRequest struct {
	Purpose    string      `json:"purpose"`
	Candidates []Candidate `json:"candidates"`
}

type RankedCandidate struct {
	ID    string  `json:"id"`
	Score float64 `json:"score"`
}

type RankResult struct {
	Items []RankedCandidate `json:"items"`
	Meta
}
