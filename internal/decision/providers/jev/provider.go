package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/decision"
)

const (
	defaultBaseURL = "https://api.typesafe.ai"
	defaultModel   = "jev-latest"
	maxBodyBytes   = 1 << 20
)

type Config struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

type Provider struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

type question struct {
	Type         string         `json:"type"`
	Instructions string         `json:"instructions"`
	Criteria     map[string]any `json:"criteria,omitempty"`
}

type request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

type answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func ConfigFromEnv() Config {
	return Config{
		APIKey:  strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY")),
		BaseURL: strings.TrimSpace(os.Getenv("TYPESAFE_BASE_URL")),
		Model:   strings.TrimSpace(os.Getenv("TYPESAFE_DEFAULT_MODEL")),
	}
}

func New(config Config) (*Provider, error) {
	config.APIKey = strings.TrimSpace(config.APIKey)
	if config.APIKey == "" {
		return nil, decision.ErrUnavailable
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
		return nil, errors.New("invalid Jev base URL")
	}
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" {
		config.Model = defaultModel
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 2 * time.Second}
	}
	return &Provider{
		apiKey:     config.APIKey,
		baseURL:    config.BaseURL,
		model:      config.Model,
		httpClient: config.HTTPClient,
	}, nil
}

func (p *Provider) Name() string { return "jev" }

func (p *Provider) Boolean(ctx context.Context, req decision.BooleanRequest) (decision.BooleanResult, error) {
	state := map[string]any{"input": req.Input}
	result, err := p.ask(ctx, state, question{
		Type:         "noul",
		Instructions: instruction(req.Purpose, "Answer whether the statement should be treated as true."),
	})
	if err != nil {
		return decision.BooleanResult{}, err
	}
	if result.Noul == nil {
		return decision.BooleanResult{}, decision.ErrInvalidResult
	}
	probability := clamp(*result.Noul)
	value := probability >= 0.5
	confidence := probability
	if !value {
		confidence = 1 - probability
	}
	return decision.BooleanResult{
		Value: value,
		Meta: decision.Meta{
			Provider:   p.Name(),
			Confidence: confidence,
			ReasonCode: "jev_noul",
		},
	}, nil
}

func (p *Provider) Choice(ctx context.Context, req decision.ChoiceRequest) (decision.ChoiceResult, error) {
	criteria, stateCandidates := choiceCandidates(req.Candidates)
	if len(criteria) == 0 {
		return decision.ChoiceResult{}, decision.ErrInvalidResult
	}
	result, err := p.ask(ctx, map[string]any{"candidates": stateCandidates}, question{
		Type:         "choice",
		Instructions: instruction(req.Purpose, "Choose the single best candidate."),
		Criteria:     criteria,
	})
	if err != nil {
		return decision.ChoiceResult{}, err
	}
	if strings.TrimSpace(result.Choice) == "" {
		return decision.ChoiceResult{}, decision.ErrInvalidResult
	}
	return decision.ChoiceResult{
		ChoiceID: result.Choice,
		Scores:   normalizedProbabilities(result.Probabilities),
		Meta: decision.Meta{
			Provider:   p.Name(),
			Confidence: clamp(result.Confidence),
			ReasonCode: "jev_choice",
		},
	}, nil
}

func (p *Provider) Score(ctx context.Context, req decision.ScoreRequest) (decision.ScoreResult, error) {
	item := normalizedCandidate(req.Item)
	if item.ID == "" {
		return decision.ScoreResult{}, decision.ErrInvalidResult
	}
	result, err := p.ask(ctx, map[string]any{
		"candidate": map[string]any{"id": item.ID, "label": item.Label},
	}, question{
		Type:         "noul",
		Instructions: instruction(req.Purpose, "Return the probability that this candidate satisfies the criterion."),
	})
	if err != nil {
		return decision.ScoreResult{}, err
	}
	if result.Noul == nil {
		return decision.ScoreResult{}, decision.ErrInvalidResult
	}
	score := clamp(*result.Noul)
	return decision.ScoreResult{
		Score: score,
		Meta: decision.Meta{
			Provider:   p.Name(),
			Confidence: confidenceFromProbability(score),
			ReasonCode: "jev_noul_score",
		},
	}, nil
}

func (p *Provider) Rank(ctx context.Context, req decision.RankRequest) (decision.RankResult, error) {
	criteria, stateCandidates := choiceCandidates(req.Candidates)
	if len(criteria) == 0 {
		return decision.RankResult{}, decision.ErrInvalidResult
	}
	result, err := p.ask(ctx, map[string]any{"candidates": stateCandidates}, question{
		Type:         "choice",
		Instructions: instruction(req.Purpose, "Estimate which candidate best satisfies the criterion; probabilities will be used as the ranking."),
		Criteria:     criteria,
	})
	if err != nil {
		return decision.RankResult{}, err
	}
	probabilities := normalizedProbabilities(result.Probabilities)
	if len(probabilities) == 0 {
		return decision.RankResult{}, decision.ErrInvalidResult
	}
	items := make([]decision.RankedCandidate, 0, len(probabilities))
	for id, score := range probabilities {
		if _, ok := criteria[id]; ok {
			items = append(items, decision.RankedCandidate{ID: id, Score: score})
		}
	}
	if len(items) == 0 {
		return decision.RankResult{}, decision.ErrInvalidResult
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ID < items[j].ID
	})
	return decision.RankResult{
		Items: items,
		Meta: decision.Meta{
			Provider:   p.Name(),
			Confidence: clamp(result.Confidence),
			ReasonCode: "jev_choice_ranking",
		},
	}, nil
}

func (p *Provider) ask(ctx context.Context, state any, q question) (answer, error) {
	payload := request{
		Model: p.model,
		State: state,
		Questions: map[string]question{
			"decision": q,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return answer{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/systemone", bytes.NewReader(raw))
	if err != nil {
		return answer{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "codelocal-decision/1")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return answer{}, err
	}
	if len(body) > maxBodyBytes {
		return answer{}, errors.New("Jev response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return answer{}, fmt.Errorf("Jev API returned %d", resp.StatusCode)
	}
	var decoded response
	if err := json.Unmarshal(body, &decoded); err != nil {
		return answer{}, decision.ErrInvalidResult
	}
	result, ok := decoded.Answers["decision"]
	if !ok {
		return answer{}, decision.ErrInvalidResult
	}
	return result, nil
}

func choiceCandidates(candidates []decision.Candidate) (map[string]any, []map[string]string) {
	criteria := make(map[string]any, len(candidates))
	state := make([]map[string]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = normalizedCandidate(candidate)
		if candidate.ID == "" || len(criteria) >= 255 {
			continue
		}
		description := candidate.Label
		if description == "" {
			description = candidate.ID
		}
		criteria[candidate.ID] = description
		state = append(state, map[string]string{"id": candidate.ID, "label": description})
	}
	return criteria, state
}

func instruction(purpose, fallback string) string {
	purpose = strings.Join(strings.Fields(purpose), " ")
	if purpose == "" {
		return fallback
	}
	return purpose
}

func normalizedCandidate(candidate decision.Candidate) decision.Candidate {
	candidate.ID = strings.TrimSpace(candidate.ID)
	candidate.Label = strings.Join(strings.Fields(candidate.Label), " ")
	return candidate
}

func normalizedProbabilities(values map[string]float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]float64, len(values))
	for key, value := range values {
		if key = strings.TrimSpace(key); key != "" {
			out[key] = clamp(value)
		}
	}
	return out
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func confidenceFromProbability(value float64) float64 {
	value = clamp(value)
	if value >= 0.5 {
		return value
	}
	return 1 - value
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
