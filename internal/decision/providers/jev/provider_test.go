package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0xmarkhydra/codelocal/internal/decision"
)

func testProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := New(Config{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestChoiceUsesSystemOneChoiceContract(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		var payload request
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		q := payload.Questions["decision"]
		if payload.Model != "jev-latest" || q.Type != "choice" || len(q.Criteria) != 2 {
			t.Fatalf("unexpected payload: %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"jev-1.13.0",
			"answers":{"decision":{
				"type":"choice",
				"choice":"code",
				"confidence":0.91,
				"probabilities":{"code":0.91,"shell":0.09}
			}},
			"usage":{"input_tokens":12,"output_tokens":0}
		}`))
	})

	result, err := provider.Choice(context.Background(), decision.ChoiceRequest{
		Purpose: "Which lane should execute this task?",
		Candidates: []decision.Candidate{
			{ID: "code", Label: "Structured code tools"},
			{ID: "shell", Label: "Guarded terminal"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChoiceID != "code" || result.Confidence != 0.91 || result.Scores["shell"] != 0.09 {
		t.Fatalf("unexpected choice: %#v", result)
	}
}

func TestRankUsesChoiceProbabilities(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"answers":{"decision":{
				"type":"choice",
				"choice":"auth",
				"confidence":0.84,
				"probabilities":{"auth":0.7,"gateway":0.2,"video":0.1}
			}}
		}`))
	})
	result, err := provider.Rank(context.Background(), decision.RankRequest{
		Purpose: "Rank files for an OAuth reconnect bug.",
		Candidates: []decision.Candidate{
			{ID: "video", Label: "video/render.go"},
			{ID: "auth", Label: "oauth/login.go"},
			{ID: "gateway", Label: "gateway/session.go"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 || result.Items[0].ID != "auth" || result.Items[1].ID != "gateway" {
		t.Fatalf("unexpected ranking: %#v", result.Items)
	}
}

func TestBooleanUsesNoulProbability(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"decision":{"type":"noul","noul":0.12}}}`))
	})
	result, err := provider.Boolean(context.Background(), decision.BooleanRequest{
		Purpose: "Is this output relevant?",
		Input:   "debug noise",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Value || result.Confidence != 0.88 {
		t.Fatalf("unexpected boolean result: %#v", result)
	}
}

func TestAPIErrorDoesNotLeakResponseBody(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret provider detail", http.StatusUnauthorized)
	})
	_, err := provider.Choice(context.Background(), decision.ChoiceRequest{
		Candidates: []decision.Candidate{{ID: "code", Label: "Code"}},
	})
	if err == nil || strings.Contains(err.Error(), "secret provider detail") {
		t.Fatalf("expected sanitized provider error, got %v", err)
	}
}

func TestNewRejectsMissingKeyAndUnsafeRemoteHTTP(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected missing-key error")
	}
	if _, err := New(Config{APIKey: "x", BaseURL: "http://example.com"}); err == nil {
		t.Fatal("expected remote HTTP URL to be rejected")
	}
}
