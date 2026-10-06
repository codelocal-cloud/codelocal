package cloudserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
	"github.com/0xmarkhydra/codelocal/internal/deviceauth"
	"github.com/0xmarkhydra/codelocal/internal/secscan"
	"github.com/0xmarkhydra/codelocal/internal/webutil"
)

const (
	securityAnalyzeMaxBody     = 512 << 10
	securityAnalyzeMaxFindings = secscan.MaxAIFindings
	securityAnalyzeMaxResponse = 2 << 20
	securityAnalyzeMaxTokens   = 8_192
	securityAnalyzeMinuteLimit = 5
	securityAnalyzeDailyLimit  = 50
	securityAnalyzeMinuteScope = "security-analyze-minute"
	securityAnalyzeDailyScope  = "security-analyze-day"
)

var (
	securityScanIDPattern      = regexp.MustCompile(`^sec_[0-9]{8}T[0-9]{6}\.[0-9]{9}Z$`)
	securityFindingIDPattern   = regexp.MustCompile(`^SEC-[0-9A-F]{10}$`)
	securityFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	securityRuleIDPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,299}$`)
	securityCWEPattern         = regexp.MustCompile(`^CWE-[0-9]+$`)
	securityCVEPattern         = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]+$`)
)

type securityRateLimitFunc func(context.Context, string, string, int, int) (bool, int, int, error)

type securityModelPayload struct {
	ScanID   string                 `json:"scanId"`
	Findings []securityModelFinding `json:"findings"`
}

type securityModelFinding struct {
	ID          string           `json:"id"`
	Fingerprint string           `json:"fingerprint"`
	Engine      string           `json:"engine"`
	RuleID      string           `json:"ruleId,omitempty"`
	Category    string           `json:"category,omitempty"`
	Severity    secscan.Severity `json:"severity"`
	StartLine   int              `json:"startLine,omitempty"`
	CWE         []string         `json:"cwe,omitempty"`
	CVE         []string         `json:"cve,omitempty"`
}

func (s *Server) securityAnalyzeAPI(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, securityAnalyzeMaxBody)
	device, err := s.authenticateDevice(r)
	if s.writeDeviceAuthFailure(w, device, err) {
		return
	}
	if !securityDeviceProofConfigured(device) {
		webutil.JSON(w, http.StatusConflict, map[string]any{
			"error":  "device_proof_failed",
			"reason": "DEVICE_SIGNATURE_REQUIRED",
		})
		return
	}

	apiKey, baseURL, model, configErr := securityLLMConfig()
	if configErr != nil || strings.TrimSpace(apiKey) == "" {
		webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security_ai_unavailable"})
		return
	}
	limited, retry, err := enforceSecurityAnalyzeRateLimits(r.Context(), device.UserID, s.Store.RateLimit)
	if err != nil {
		webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "security_rate_limit_unavailable"})
		return
	}
	if limited {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retry))
		webutil.JSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate_limited", "retry_after": retry})
		return
	}

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var payload secscan.AIPayload
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_security_payload"})
		return
	}
	if err := validateSecurityAIPayload(payload); err != nil {
		webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_security_payload"})
		return
	}
	if len(payload.Findings) == 0 {
		webutil.JSON(w, http.StatusOK, secscan.AIResponse{Provider: "codelocal", Results: []secscan.AIResult{}})
		return
	}
	payload = sanitizeSecurityAIPayload(payload)

	raw, _ := json.Marshal(buildSecurityModelPayload(payload))
	systemPrompt := strings.Join([]string{
		"You are CodeLocal Security, a defensive application-security triage assistant.",
		"You receive normalized findings from local security scanners. The payload intentionally excludes raw secrets and source-code evidence.",
		"For each finding, decide whether it is confirmed, needs_review, or not_actionable using only the supplied metadata.",
		"Do not invent source code, exploit steps, credentials, or evidence that is not present.",
		"Return remediation-focused analysis.",
		"Respond with JSON only in this exact shape:",
		"{\"results\":[{\"id\":\"SEC-...\",\"status\":\"confirmed|needs_review|not_actionable\",\"confidence\":0.0,\"exploitable\":false,\"explanation\":\"short reason\",\"recommendedFix\":\"short defensive fix\",\"priority\":\"immediate|high|normal|low\"}]}",
	}, "\n")
	messages := []map[string]any{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": string(raw)},
	}

	aiContent, err := callSecurityLLM(securityProtocolForModel(baseURL, model), baseURL, apiKey, model, messages)
	if err != nil {
		webutil.JSON(w, http.StatusBadGateway, map[string]string{"error": "security_ai_failed"})
		return
	}
	response, err := parseSecurityAIResponse(aiContent, payload)
	if err != nil {
		webutil.JSON(w, http.StatusBadGateway, map[string]string{"error": "security_ai_invalid_response"})
		return
	}
	response.Provider = "codelocal"
	s.Store.Audit(cloud.AuditEvent{
		UserID:   device.UserID,
		Event:    "security.scan_analyzed",
		DeviceID: device.DeviceID,
		Detail: map[string]any{
			"scanId":   payload.ScanID,
			"findings": len(payload.Findings),
		},
	})
	webutil.JSON(w, http.StatusOK, response)
}

func securityLLMConfig() (apiKey, baseURL, model string, err error) {
	apiKey = strings.TrimSpace(os.Getenv("CODELOCAL_SECURITY_LLM_API_KEY"))
	baseURL = strings.TrimSpace(os.Getenv("CODELOCAL_SECURITY_LLM_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model = strings.TrimSpace(os.Getenv("CODELOCAL_SECURITY_LLM_MODEL"))
	if model == "" {
		model = "gpt-4o-mini"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	parsed, parseErr := url.Parse(baseURL)
	if parseErr != nil || parsed.Host == "" || parsed.Scheme != "https" {
		return "", "", "", fmt.Errorf("invalid Security LLM base URL")
	}
	return apiKey, baseURL, model, nil
}

func securityDeviceProofConfigured(device *cloud.Device) bool {
	return device != nil && deviceauth.ValidPublicKey(device.PublicKey)
}

func validateSecurityAIPayload(payload secscan.AIPayload) error {
	if payload.SchemaVersion != 1 ||
		!securityScanIDPattern.MatchString(payload.ScanID) ||
		len(payload.Findings) > securityAnalyzeMaxFindings {
		return fmt.Errorf("invalid security payload")
	}
	for _, finding := range payload.Findings {
		if !securityFindingIDPattern.MatchString(finding.ID) ||
			!securityFingerprintPattern.MatchString(finding.Fingerprint) ||
			!securityRuleIDPattern.MatchString(finding.RuleID) ||
			!validSecurityEngineCategory(finding.Engine, finding.Category) ||
			!validSecuritySeverity(finding.Severity) ||
			!validSecurityBoundedText(finding.File, 800) ||
			finding.StartLine < 0 ||
			finding.EndLine < 0 ||
			len(finding.CWE) > 16 ||
			len(finding.CVE) > 16 {
			return fmt.Errorf("invalid security finding")
		}
		for _, value := range finding.CWE {
			if len(value) > 40 || !securityCWEPattern.MatchString(value) {
				return fmt.Errorf("invalid CWE")
			}
		}
		for _, value := range finding.CVE {
			if len(value) > 40 || !securityCVEPattern.MatchString(value) {
				return fmt.Errorf("invalid CVE")
			}
		}
		for key := range finding.Metadata {
			if finding.Engine != "trivy" ||
				(key != "package" && key != "installedVersion" && key != "fixedVersion") ||
				!validSecurityBoundedText(finding.Metadata[key], 300) {
				return fmt.Errorf("invalid security metadata")
			}
		}
	}
	return nil
}

func validSecurityBoundedText(value string, maxLength int) bool {
	if len(value) > maxLength {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validSecurityEngineCategory(engine, category string) bool {
	switch engine {
	case "gitleaks":
		return category == "secret"
	case "trivy":
		return category == "dependency-vulnerability" || category == "misconfiguration"
	case "semgrep":
		return category == "source-code"
	default:
		return false
	}
}

func validSecuritySeverity(severity secscan.Severity) bool {
	switch severity {
	case secscan.SeverityCritical, secscan.SeverityHigh, secscan.SeverityMedium, secscan.SeverityLow, secscan.SeverityUnknown:
		return true
	default:
		return false
	}
}

func securityProtocolForModel(baseURL, model string) dashboardLLMProtocol {
	name := strings.ToLower(strings.TrimSpace(model))
	normalizedBaseURL := strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if strings.HasPrefix(name, "muse-") {
		return dashboardProtocolResponses
	}
	if strings.Contains(normalizedBaseURL, "opencode.ai/zen") {
		switch {
		case strings.HasPrefix(name, "gpt-"), strings.HasPrefix(name, "grok-"):
			return dashboardProtocolResponses
		case strings.HasPrefix(name, "deepseek-"), strings.HasPrefix(name, "minimax-"), strings.HasPrefix(name, "glm-"), strings.HasPrefix(name, "kimi-"), strings.HasPrefix(name, "nemotron-"), strings.HasPrefix(name, "mimo-"), strings.HasPrefix(name, "hy3-"), strings.HasPrefix(name, "x-preview-"), name == "big-pickle":
			return dashboardProtocolChatCompletions
		default:
			return dashboardProtocolUnsupported
		}
	}
	if strings.Contains(normalizedBaseURL, "api.shopaikey.com") && strings.HasPrefix(name, "gpt-") {
		return dashboardProtocolResponses
	}
	return dashboardProtocolChatCompletions
}

func callSecurityLLM(protocol dashboardLLMProtocol, baseURL, apiKey, model string, messages []map[string]any) (string, error) {
	var endpoint string
	var body map[string]any
	switch protocol {
	case dashboardProtocolResponses:
		endpoint = baseURL + "/responses"
		body = map[string]any{
			"model":             model,
			"input":             responsesInput(messages),
			"max_output_tokens": securityAnalyzeMaxTokens,
		}
	case dashboardProtocolChatCompletions:
		endpoint = baseURL + "/chat/completions"
		body = map[string]any{
			"model":       model,
			"messages":    messages,
			"temperature": 0,
			"max_tokens":  securityAnalyzeMaxTokens,
		}
	default:
		return "", fmt.Errorf("unsupported Security LLM protocol")
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	response, err := dashboardLLMHTTPClient(45 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, securityAnalyzeMaxResponse+1))
	if err != nil {
		return "", err
	}
	if len(raw) > securityAnalyzeMaxResponse {
		return "", fmt.Errorf("Security LLM response too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &httpError{Status: response.StatusCode, Body: string(raw)}
	}
	if protocol == dashboardProtocolChatCompletions {
		var data struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			return "", err
		}
		if len(data.Choices) == 0 {
			return "", fmt.Errorf("Security LLM returned no choices")
		}
		return data.Choices[0].Message.Content, nil
	}
	var data struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", err
	}
	var content strings.Builder
	for _, item := range data.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" {
				content.WriteString(part.Text)
			}
		}
	}
	if content.Len() == 0 {
		return "", fmt.Errorf("Security LLM returned no output text")
	}
	return content.String(), nil
}

func enforceSecurityAnalyzeRateLimits(ctx context.Context, userID string, rateLimit securityRateLimitFunc) (limited bool, retry int, err error) {
	rules := []struct {
		scope  string
		limit  int
		window int
	}{
		{scope: securityAnalyzeMinuteScope, limit: securityAnalyzeMinuteLimit, window: 60},
		{scope: securityAnalyzeDailyScope, limit: securityAnalyzeDailyLimit, window: 24 * 60 * 60},
	}
	for _, rule := range rules {
		allowed, _, retry, err := rateLimit(ctx, rule.scope, userID, rule.limit, rule.window)
		if err != nil {
			return false, 0, err
		}
		if !allowed {
			return true, max(1, retry), nil
		}
	}
	return false, 0, nil
}

func sanitizeSecurityAIPayload(payload secscan.AIPayload) secscan.AIPayload {
	payload = secscan.SanitizeAIPayload(payload)
	payload.Project = ""
	payload.Branch = ""
	payload.Commit = ""
	payload.Summary = secscan.Summary{}
	for index := range payload.Findings {
		finding := &payload.Findings[index]
		finding.ID = boundedSecurityText(finding.ID, 80)
		finding.Fingerprint = boundedSecurityText(finding.Fingerprint, 160)
		finding.Engine = boundedSecurityText(finding.Engine, 80)
		finding.RuleID = boundedSecurityText(finding.RuleID, 300)
		finding.Category = boundedSecurityText(finding.Category, 120)
		finding.Title = ""
		finding.Description = ""
		finding.Recommendation = ""
		finding.File = ""
		finding.CWE = boundedSecurityTexts(finding.CWE, 16, 40)
		finding.CVE = boundedSecurityTexts(finding.CVE, 16, 40)
		if finding.StartLine < 0 {
			finding.StartLine = 0
		}
		if finding.EndLine < 0 {
			finding.EndLine = 0
		}
		finding.Metadata = nil
	}
	return payload
}

func buildSecurityModelPayload(payload secscan.AIPayload) securityModelPayload {
	modelPayload := securityModelPayload{
		ScanID:   payload.ScanID,
		Findings: make([]securityModelFinding, 0, len(payload.Findings)),
	}
	for _, finding := range payload.Findings {
		modelPayload.Findings = append(modelPayload.Findings, securityModelFinding{
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
	return modelPayload
}

func boundedSecurityTexts(values []string, maxItems, maxLength int) []string {
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	for index := range values {
		values[index] = boundedSecurityText(values[index], maxLength)
	}
	return values
}

func boundedSecurityText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func parseSecurityAIResponse(content string, payload secscan.AIPayload) (secscan.AIResponse, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return secscan.AIResponse{}, fmt.Errorf("AI response did not contain JSON")
	}

	var response secscan.AIResponse
	if err := json.Unmarshal([]byte(content[start:end+1]), &response); err != nil {
		return secscan.AIResponse{}, err
	}
	allowed := make(map[string]struct{}, len(payload.Findings))
	for _, finding := range payload.Findings {
		allowed[finding.ID] = struct{}{}
	}
	filtered := make([]secscan.AIResult, 0, len(response.Results))
	seen := make(map[string]struct{}, len(response.Results))
	for _, result := range response.Results {
		if _, ok := allowed[result.ID]; !ok {
			continue
		}
		if _, duplicate := seen[result.ID]; duplicate {
			continue
		}
		seen[result.ID] = struct{}{}
		switch result.Status {
		case "confirmed", "needs_review", "not_actionable":
		default:
			result.Status = "needs_review"
		}
		if result.Confidence < 0 {
			result.Confidence = 0
		}
		if result.Confidence > 1 {
			result.Confidence = 1
		}
		result.Explanation = boundedSecurityText(result.Explanation, 2400)
		result.RecommendedFix = boundedSecurityText(result.RecommendedFix, 2400)
		result.Priority = boundedSecurityText(result.Priority, 40)
		filtered = append(filtered, result)
	}
	response.Results = filtered
	return response, nil
}
