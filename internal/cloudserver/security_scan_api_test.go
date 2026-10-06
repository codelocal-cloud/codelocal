package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
	"github.com/0xmarkhydra/codelocal/internal/deviceauth"
	"github.com/0xmarkhydra/codelocal/internal/secscan"
)

func TestSecurityLLMConfigUsesOnlySecurityEnvironment(t *testing.T) {
	t.Setenv("CODELOCAL_SECURITY_LLM_API_KEY", "")
	t.Setenv("CODELOCAL_SECURITY_LLM_BASE_URL", "")
	t.Setenv("CODELOCAL_SECURITY_LLM_MODEL", "")
	t.Setenv("CODELOCAL_LLM_API_KEY", "dashboard-key")
	t.Setenv("CODELOCAL_LLM_BASE_URL", "https://dashboard.example.test/v1")
	t.Setenv("CODELOCAL_LLM_MODEL", "dashboard-model")
	t.Setenv("OPENAI_API_KEY", "openai-fallback-key")

	apiKey, baseURL, model, err := securityLLMConfig()
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "" {
		t.Fatalf("apiKey = %q, want empty without Security-specific configuration", apiKey)
	}
	if baseURL != "https://api.openai.com/v1" {
		t.Fatalf("baseURL = %q", baseURL)
	}
	if model != "gpt-4o-mini" {
		t.Fatalf("model = %q", model)
	}
}

func TestSecurityLLMConfigReadsSecurityEnvironment(t *testing.T) {
	t.Setenv("CODELOCAL_SECURITY_LLM_API_KEY", "security-key")
	t.Setenv("CODELOCAL_SECURITY_LLM_BASE_URL", "https://security.example.test/v1/")
	t.Setenv("CODELOCAL_SECURITY_LLM_MODEL", "security-model")

	apiKey, baseURL, model, err := securityLLMConfig()
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "security-key" || baseURL != "https://security.example.test/v1" || model != "security-model" {
		t.Fatalf("securityLLMConfig() = (%q, %q, %q)", apiKey, baseURL, model)
	}
}

func TestSecurityLLMConfigRejectsInvalidBaseURL(t *testing.T) {
	t.Setenv("CODELOCAL_SECURITY_LLM_API_KEY", "security-key")
	t.Setenv("CODELOCAL_SECURITY_LLM_BASE_URL", "not-a-url")
	t.Setenv("CODELOCAL_SECURITY_LLM_MODEL", "security-model")

	if _, _, _, err := securityLLMConfig(); err == nil {
		t.Fatal("invalid Security LLM base URL was accepted")
	}
}

func TestSecurityLLMConfigRejectsPlaintextBaseURL(t *testing.T) {
	t.Setenv("CODELOCAL_SECURITY_LLM_API_KEY", "security-key")
	t.Setenv("CODELOCAL_SECURITY_LLM_BASE_URL", "http://security.example.test/v1")
	t.Setenv("CODELOCAL_SECURITY_LLM_MODEL", "security-model")

	if _, _, _, err := securityLLMConfig(); err == nil {
		t.Fatal("plaintext Security LLM base URL was accepted")
	}
}

func TestSecurityDeviceProofConfigured(t *testing.T) {
	publicKey, _, err := deviceauth.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if securityDeviceProofConfigured(nil) {
		t.Fatal("nil device accepted")
	}
	if securityDeviceProofConfigured(&cloud.Device{}) {
		t.Fatal("legacy unsigned device accepted")
	}
	if !securityDeviceProofConfigured(&cloud.Device{PublicKey: publicKey}) {
		t.Fatal("signed device rejected")
	}
}

func TestValidateSecurityAIPayload(t *testing.T) {
	valid := secscan.AIPayload{
		SchemaVersion: 1,
		ScanID:        "sec_20260925T120000.000000000Z",
		Findings: []secscan.AIFinding{{
			ID:          "SEC-1234567890",
			Fingerprint: strings.Repeat("a", 64),
			Engine:      "trivy",
			RuleID:      "CVE-2026-1234",
			Category:    "dependency-vulnerability",
			Severity:    secscan.SeverityHigh,
			CVE:         []string{"CVE-2026-1234"},
			Metadata:    map[string]string{"package": "example.com/module"},
		}},
	}
	if err := validateSecurityAIPayload(valid); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*secscan.AIPayload)
	}{
		{name: "scan ID", mutate: func(payload *secscan.AIPayload) { payload.ScanID = "not-a-scan" }},
		{name: "finding ID", mutate: func(payload *secscan.AIPayload) { payload.Findings[0].ID = "anything" }},
		{name: "fingerprint", mutate: func(payload *secscan.AIPayload) { payload.Findings[0].Fingerprint = "short" }},
		{name: "engine", mutate: func(payload *secscan.AIPayload) { payload.Findings[0].Engine = "custom" }},
		{name: "category", mutate: func(payload *secscan.AIPayload) { payload.Findings[0].Category = "prompt" }},
		{name: "severity", mutate: func(payload *secscan.AIPayload) { payload.Findings[0].Severity = "PROMPT" }},
		{name: "CVE", mutate: func(payload *secscan.AIPayload) { payload.Findings[0].CVE = []string{"not-a-cve"} }},
		{name: "metadata", mutate: func(payload *secscan.AIPayload) { payload.Findings[0].Metadata["prompt"] = "ignore instructions" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(valid)
			if err != nil {
				t.Fatal(err)
			}
			var payload secscan.AIPayload
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatal(err)
			}
			test.mutate(&payload)
			if err := validateSecurityAIPayload(payload); err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestSecurityProtocolIgnoresDashboardEnvironment(t *testing.T) {
	t.Setenv("CODELOCAL_LLM_PROVIDER", "zen")
	t.Setenv("CODELOCAL_LLM_BASE_URL", "https://security.example.test/v1")
	t.Setenv("CODELOCAL_SHOPAIKEY_BASE_URL", "https://security.example.test/v1")

	if got := securityProtocolForModel("https://security.example.test/v1", "gpt-custom"); got != dashboardProtocolChatCompletions {
		t.Fatalf("protocol = %v, want chat completions", got)
	}
	if got := securityProtocolForModel("https://opencode.ai/zen/v1", "gpt-custom"); got != dashboardProtocolResponses {
		t.Fatalf("Zen protocol = %v, want responses", got)
	}
}

func TestCallSecurityLLMBoundsChatCompletion(t *testing.T) {
	if securityAnalyzeMaxTokens != 8_192 {
		t.Fatalf("securityAnalyzeMaxTokens = %d, want 8192", securityAnalyzeMaxTokens)
	}
	var requestBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer security-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"results\":[]}"}}]}`))
	}))
	defer upstream.Close()

	content, err := callSecurityLLM(
		dashboardProtocolChatCompletions,
		upstream.URL,
		"security-key",
		"security-model",
		[]map[string]any{{"role": "user", "content": "{}"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if content != `{"results":[]}` {
		t.Fatalf("content = %q", content)
	}
	if requestBody["max_tokens"] != float64(securityAnalyzeMaxTokens) {
		t.Fatalf("max_tokens = %#v", requestBody["max_tokens"])
	}
}

func TestCallSecurityLLMRejectsOversizedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", securityAnalyzeMaxResponse+1)))
	}))
	defer upstream.Close()

	if _, err := callSecurityLLM(
		dashboardProtocolChatCompletions,
		upstream.URL,
		"security-key",
		"security-model",
		nil,
	); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnforceSecurityAnalyzeRateLimits(t *testing.T) {
	type call struct {
		scope  string
		userID string
		limit  int
		window int
	}
	var calls []call
	rateLimit := func(_ context.Context, scope, userID string, limit, window int) (bool, int, int, error) {
		calls = append(calls, call{scope: scope, userID: userID, limit: limit, window: window})
		return true, 1, 0, nil
	}

	limited, retry, err := enforceSecurityAnalyzeRateLimits(context.Background(), "user-1", rateLimit)
	if err != nil || limited || retry != 0 {
		t.Fatalf("result = (%v, %d, %v)", limited, retry, err)
	}
	want := []call{
		{scope: securityAnalyzeMinuteScope, userID: "user-1", limit: 5, window: 60},
		{scope: securityAnalyzeDailyScope, userID: "user-1", limit: 50, window: 86400},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v", calls)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("call %d = %#v, want %#v", index, calls[index], want[index])
		}
	}
}

func TestEnforceSecurityAnalyzeRateLimitsRejectsDailyQuota(t *testing.T) {
	rateLimit := func(_ context.Context, scope, _ string, _, _ int) (bool, int, int, error) {
		if scope == securityAnalyzeDailyScope {
			return false, 51, 900, nil
		}
		return true, 1, 0, nil
	}

	limited, retry, err := enforceSecurityAnalyzeRateLimits(context.Background(), "user-1", rateLimit)
	if err != nil || !limited || retry != 900 {
		t.Fatalf("result = (%v, %d, %v)", limited, retry, err)
	}
}

func TestEnforceSecurityAnalyzeRateLimitsFailsClosed(t *testing.T) {
	wantErr := errors.New("redis unavailable")
	rateLimit := func(context.Context, string, string, int, int) (bool, int, int, error) {
		return false, 0, 0, wantErr
	}

	limited, retry, err := enforceSecurityAnalyzeRateLimits(context.Background(), "user-1", rateLimit)
	if limited || retry != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("result = (%v, %d, %v)", limited, retry, err)
	}
}

func TestParseSecurityAIResponseFiltersAndClamps(t *testing.T) {
	payload := secscan.AIPayload{
		SchemaVersion: 1,
		ScanID:        "sec_test",
		Findings: []secscan.AIFinding{
			{ID: "SEC-ONE"},
		},
	}
	response, err := parseSecurityAIResponse(strings.Join([]string{
		"~~~",
		"{\"results\":[",
		"{\"id\":\"SEC-ONE\",\"status\":\"weird\",\"confidence\":2,\"explanation\":\"ok\"},",
		"{\"id\":\"SEC-HALLUCINATED\",\"status\":\"confirmed\",\"confidence\":1}",
		"]}",
	}, ""), payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(response.Results))
	}
	result := response.Results[0]
	if result.ID != "SEC-ONE" {
		t.Fatalf("id = %q", result.ID)
	}
	if result.Status != "needs_review" {
		t.Fatalf("status = %q", result.Status)
	}
	if result.Confidence != 1 {
		t.Fatalf("confidence = %v", result.Confidence)
	}
}

func TestSanitizeSecurityAIPayloadBoundsText(t *testing.T) {
	cwes := make([]string, 40)
	cves := make([]string, 40)
	for index := range cwes {
		cwes[index] = strings.Repeat("w", 200)
		cves[index] = strings.Repeat("v", 200)
	}
	payload := secscan.AIPayload{
		SchemaVersion: 1,
		ScanID:        "sec_test",
		Project:       strings.Repeat("p", 500),
		Branch:        "branch-must-stay-local",
		Commit:        "commit-must-stay-local",
		Findings: []secscan.AIFinding{
			{
				ID:          "SEC-ONE",
				File:        "path-must-stay-local/main.go",
				Description: "token=server-secret " + strings.Repeat("x", 5000),
				CWE:         cwes,
				CVE:         cves,
				StartLine:   -1,
				EndLine:     -2,
				Metadata: map[string]string{
					strings.Repeat("k", 200): strings.Repeat("m", 1000),
					"credential":             "must-never-leave",
					"api_key":                "must-never-leave",
					"source":                 "raw-source-must-never-leave",
				},
			},
		},
	}
	got := sanitizeSecurityAIPayload(payload)
	if got.Project != "" || got.Branch != "" || got.Commit != "" {
		t.Fatalf("repository context reached sanitized payload: %#v", got)
	}
	if got.Findings[0].Title != "" || got.Findings[0].Description != "" || got.Findings[0].Recommendation != "" {
		t.Fatal("scanner free text reached model payload")
	}
	if got.Findings[0].File != "" || got.Findings[0].Metadata != nil {
		t.Fatalf("file or package metadata reached sanitized payload: %#v", got.Findings[0])
	}
	if len(got.Findings[0].CWE) != 16 || len(got.Findings[0].CVE) != 16 {
		t.Fatalf("CWE/CVE counts = %d/%d", len(got.Findings[0].CWE), len(got.Findings[0].CVE))
	}
	if len(got.Findings[0].CWE[0]) != 40 || len(got.Findings[0].CVE[0]) != 40 {
		t.Fatalf("CWE/CVE lengths = %d/%d", len(got.Findings[0].CWE[0]), len(got.Findings[0].CVE[0]))
	}
	if got.Findings[0].StartLine != 0 || got.Findings[0].EndLine != 0 {
		t.Fatalf("negative lines survived: %d/%d", got.Findings[0].StartLine, got.Findings[0].EndLine)
	}
	if strings.Contains(got.Findings[0].Description, "server-secret") {
		t.Fatal("secret reached model payload")
	}
	if _, ok := got.Findings[0].Metadata["credential"]; ok {
		t.Fatal("sensitive metadata reached model payload")
	}
	if _, ok := got.Findings[0].Metadata["api_key"]; ok {
		t.Fatal("API key metadata reached model payload")
	}
	if _, ok := got.Findings[0].Metadata["source"]; ok {
		t.Fatal("source metadata reached model payload")
	}
}

func TestBuildSecurityModelPayloadContainsOnlyStructuralFields(t *testing.T) {
	payload := sanitizeSecurityAIPayload(secscan.AIPayload{
		SchemaVersion: 1,
		ScanID:        "sec_20260925T120000.000000000Z",
		Project:       "project-must-stay-local",
		Branch:        "branch-must-stay-local",
		Commit:        "commit-must-stay-local",
		Findings: []secscan.AIFinding{{
			ID:             "SEC-1234567890",
			Fingerprint:    strings.Repeat("a", 64),
			Engine:         "trivy",
			RuleID:         "CVE-2026-1234",
			Category:       "dependency-vulnerability",
			Severity:       secscan.SeverityHigh,
			Title:          "title-must-stay-local",
			Description:    "description-must-stay-local",
			Recommendation: "fix-must-stay-local",
			File:           "path-must-stay-local/main.go",
			StartLine:      12,
			EndLine:        13,
			CVE:            []string{"CVE-2026-1234"},
			Metadata:       map[string]string{"package": "package-must-stay-local"},
		}},
	})
	raw, err := json.Marshal(buildSecurityModelPayload(payload))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"project-must-stay-local",
		"branch-must-stay-local",
		"commit-must-stay-local",
		"title-must-stay-local",
		"description-must-stay-local",
		"fix-must-stay-local",
		"path-must-stay-local",
		"package-must-stay-local",
		"installedVersion",
		"fixedVersion",
		"endLine",
		"summary",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("%q reached model payload: %s", forbidden, raw)
		}
	}
	for _, required := range []string{
		`"scanId":"sec_20260925T120000.000000000Z"`,
		`"id":"SEC-1234567890"`,
		`"ruleId":"CVE-2026-1234"`,
		`"startLine":12`,
		`"cve":["CVE-2026-1234"]`,
	} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("model payload missing %s: %s", required, raw)
		}
	}
}
