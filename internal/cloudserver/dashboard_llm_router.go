package cloudserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
)

const (
	dashboardModelAuto       = "auto"
	dashboardModelGLM        = "glm-5.3-flash"
	dashboardModelQwen       = "qwen3.8-flash"
	dashboardModelMuse       = "muse-spark-1.3-contributor-free"
	dashboardModelMuseLegacy = "muse-spark-1.2-contributor-free"
)

type dashboardLLMTarget struct {
	ID          string
	BaseURL     string
	APIKey      string
	APIKeyBytes []byte
	Model       string
	Protocol    dashboardLLMProtocol
	Community   bool
	// Vision marks chat targets that accept image_url/input_image blocks.
	// Community text-only targets stay false.
	Vision bool
}

func dashboardTargetProtocol(target dashboardLLMTarget) dashboardLLMProtocol {
	if target.Protocol != dashboardProtocolUnsupported {
		return target.Protocol
	}
	return dashboardProtocolForModel(target.BaseURL, target.Model)
}

func dashboardTargetAPIKey(target dashboardLLMTarget) string {
	if len(target.APIKeyBytes) > 0 {
		return string(target.APIKeyBytes)
	}
	return target.APIKey
}

type dashboardSelectedModelError struct {
	Err error
}

func (e *dashboardSelectedModelError) Error() string {
	return "CodeLocal đã thử lại các route khả dụng nhưng hiện chưa có route nào sẵn sàng để tiếp tục. Vui lòng thử lại sau ít giây."
}

func (e *dashboardSelectedModelError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func dashboardRouteError(selection string, err error) error {
	if dashboardNormalizeModelSelection(selection) == dashboardModelAuto {
		return err
	}
	if err == nil {
		err = errors.New("selected model route is unavailable")
	}
	return &dashboardSelectedModelError{Err: err}
}

var dashboardLLMHealth = struct {
	sync.Mutex
	cooldownUntil map[string]time.Time
}{cooldownUntil: map[string]time.Time{}}

func dashboardNormalizeModelSelection(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if _, _, ok := dashboardParseUserModelSelection(trimmed); ok {
		return trimmed
	}
	switch strings.ToLower(trimmed) {
	case "", "auto":
		return dashboardModelAuto
	case dashboardModelGLM, "glm 5.3 flash", "glm-5.3-flash-20260826":
		return dashboardModelGLM
	case dashboardModelQwen, "qwen 3.8 flash", "qwen3.8-flash-next":
		return dashboardModelQwen
	case dashboardModelMuse, "muse spark 1.3", "muse-spark-1.3":
		return dashboardModelMuse
	case dashboardModelMuseLegacy, "muse spark 1.2", "muse-spark-1.2":
		return dashboardModelMuseLegacy
	default:
		if dashboardModelIDSafe(trimmed) {
			return trimmed
		}
		return dashboardModelAuto
	}
}

func dashboardModelSupportsVision(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	switch {
	case name == dashboardModelMuse,
		name == dashboardModelMuseLegacy,
		name == dashboardModelGLM,
		name == dashboardModelQwen:
		// Explicit vision routing (Task 1): community sparklines stay
		// text-only so image requests never default to a target that drops
		// image_url/input_image blocks.
		return false
	case strings.HasPrefix(name, "gpt-"),
		strings.HasPrefix(name, "gemini-"),
		strings.HasPrefix(name, "claude-"),
		strings.HasPrefix(name, "kimi-"),
		strings.HasPrefix(name, "qwen"):
		return true
	default:
		return false
	}
}

// dashboardVisionModelPreference ranks vision-capable candidates for image
// requests: Shop defaults first, then detected Shop catalog models.
func dashboardVisionModelPreference() []string {
	preferred := make([]string, 0, 4)
	seen := map[string]bool{}
	appendModel := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		preferred = append(preferred, model)
	}
	if _, baseURL, defaultModel, ok := dashboardShopAIKeyConfig(); ok {
		if dashboardModelSupportsVision(defaultModel) {
			appendModel(defaultModel)
		}
		_ = baseURL
	}
	for _, fallback := range []string{"gpt-5.6-sol", "gpt-image-1.5", "gemini-2.5-flash-image"} {
		if dashboardModelSupportsVision(fallback) {
			appendModel(fallback)
		}
	}
	return preferred
}

// dashboardVisionRoute builds a strict vision-capable route for image
// requests. It prefers Shop vision models and never falls back to text-only
// community sparklines.
func dashboardVisionRoute(selection string) []dashboardLLMTarget {
	ordered := make([]dashboardLLMTarget, 0, 4)
	appendTarget := func(target dashboardLLMTarget) {
		if !target.Vision {
			return
		}
		for _, existing := range ordered {
			if existing.BaseURL == target.BaseURL && existing.Model == target.Model {
				return
			}
		}
		ordered = append(ordered, target)
	}
	if selection != dashboardModelAuto {
		if shop, ok := dashboardShopAIKeyTarget(selection); ok {
			appendTarget(shop)
		}
	}
	for _, candidate := range dashboardVisionModelPreference() {
		if shop, ok := dashboardShopAIKeyTarget(candidate); ok {
			appendTarget(shop)
		}
	}
	return ordered
}

// dashboardVisionBlockedMessage explains why an image request has no vision
// route instead of falling back to the generic mock reply.
func dashboardVisionBlockedMessage() string {
	return "The selected AI provider/model does not support images. Configure a vision-capable model in AI providers and try again."
}

func dashboardEmperoTarget(model string) dashboardLLMTarget {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CODELOCAL_EMPERO_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://free.empero.org/v1"
	}
	apiKey := strings.TrimSpace(os.Getenv("CODELOCAL_EMPERO_API_KEY"))
	if apiKey == "" {
		apiKey = "free"
	}
	return dashboardLLMTarget{ID: "empero:" + model, BaseURL: baseURL, APIKey: apiKey, Model: model, Community: true}
}

func dashboardMuseTarget(model string) (dashboardLLMTarget, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = dashboardModelMuse
	}
	if model != dashboardModelMuse && model != dashboardModelMuseLegacy {
		return dashboardLLMTarget{}, false
	}
	apiKey := strings.TrimSpace(os.Getenv("OPENCODE_ZEN_API_KEY"))
	baseURL := "https://opencode.ai/zen/v1"
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CODELOCAL_LLM_PROVIDER")), "zen") {
		if configured := strings.TrimSpace(os.Getenv("CODELOCAL_LLM_API_KEY")); configured != "" {
			apiKey = configured
		}
		if configured := strings.TrimSpace(os.Getenv("CODELOCAL_LLM_BASE_URL")); configured != "" {
			baseURL = strings.TrimRight(configured, "/")
		}
	}
	if apiKey == "" {
		return dashboardLLMTarget{}, false
	}
	return dashboardLLMTarget{ID: "zen:" + model, BaseURL: baseURL, APIKey: apiKey, Model: model, Community: true}, true
}

// dashboardZenLanePinned reports whether dashboard chat is pinned to the
// direct OpenCode Zen lane. When Zen is explicitly configured with a
// credential, the model picker offers only Auto and Muse Spark 1.3 instead of
// the ShopAIKey/curated catalogs.
func dashboardZenLanePinned() bool {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("CODELOCAL_LLM_PROVIDER")), "zen") {
		return false
	}
	_, ok := dashboardMuseTarget(dashboardModelMuse)
	return ok
}

// dashboardCommunityBlockedMessage explains why a request still has no route
// even though providers are configured: community models must not receive
// workspace-bound, image, or sensitive content.
func dashboardCommunityBlockedMessage() string {
	return "Model community miễn phí (Muse 1.3, GLM, Qwen) không nhận nội dung gắn workspace, ảnh hoặc thông tin nhạy cảm. Bạn bỏ workspace đang chọn rồi gửi lại, hoặc chuyển model sang Auto để tiếp tục."
}

func dashboardLegacyTarget() (dashboardLLMTarget, bool) {
	apiKey, baseURL, model := dashboardLLMConfig()
	if strings.TrimSpace(apiKey) == "" {
		return dashboardLLMTarget{}, false
	}
	community := strings.Contains(strings.ToLower(baseURL), "free.empero.org") ||
		(strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "muse-") && dashboardUsesZen(baseURL))
	return dashboardLLMTarget{ID: "legacy:" + model, BaseURL: baseURL, APIKey: apiKey, Model: model, Community: community}, true
}

func dashboardLLMRoute(selection string, allowCommunity bool) []dashboardLLMTarget {
	selection = dashboardNormalizeModelSelection(selection)
	ordered := make([]dashboardLLMTarget, 0, 7)
	appendTarget := func(target dashboardLLMTarget) {
		if target.Community && !allowCommunity {
			return
		}
		for _, existing := range ordered {
			if existing.BaseURL == target.BaseURL && existing.Model == target.Model {
				return
			}
		}
		ordered = append(ordered, target)
	}

	glm := dashboardEmperoTarget(dashboardModelGLM)
	qwen := dashboardEmperoTarget(dashboardModelQwen)
	muse, hasMuse := dashboardMuseTarget(dashboardModelMuse)
	shopDefault, hasShop := dashboardShopAIKeyTarget("")
	switch selection {
	case dashboardModelGLM:
		appendTarget(glm)
	case dashboardModelQwen:
		appendTarget(qwen)
	case dashboardModelMuse, dashboardModelMuseLegacy:
		if directMuse, ok := dashboardMuseTarget(selection); ok {
			appendTarget(directMuse)
		}
	case dashboardModelAuto:
		if hasShop {
			appendTarget(shopDefault)
		}
		appendTarget(glm)
		appendTarget(qwen)
		if hasMuse {
			appendTarget(muse)
		}
		if legacy, ok := dashboardLegacyTarget(); ok {
			appendTarget(legacy)
		}
	default:
		if shop, ok := dashboardShopAIKeyTarget(selection); ok {
			appendTarget(shop)
		}
	}
	return ordered
}

func dashboardLLMRouteWithContext(ctx context.Context, selection string, allowCommunity, _ bool) []dashboardLLMTarget {
	selection = dashboardNormalizeModelSelection(selection)
	if targets, ok := dashboardUserProviderRoutesFromContext(ctx, selection); ok {
		return targets
	}
	return dashboardLLMRoute(selection, allowCommunity)
}

func dashboardLooksSensitive(value string) bool {
	lower := strings.ToLower(value)
	for _, pattern := range []string{"authorization: bearer", "-----begin private key-----", "api_key=", "apikey=", "access_token=", "refresh_token=", "token=", "secret=", "password=", "client_secret", "private_key", ".env"} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func dashboardCommunityEligible(req dashboardChatRequest) bool {
	if strings.TrimSpace(req.Image) != "" || req.ImageMeta != nil || req.Workspace != nil || dashboardLooksSensitive(req.Message) {
		return false
	}
	for _, item := range req.History {
		if dashboardLooksSensitive(item.Content) {
			return false
		}
	}
	return true
}

// dashboardCommunityWorkspaceAllowed reports whether the deployment explicitly
// opts into sending workspace-bound and image content to community lanes via
// CODELOCAL_ALLOW_COMMUNITY_WORKSPACE=1. Default off. Enabling it lets free
// providers receive project context; obvious secrets stay blocked.
func dashboardCommunityWorkspaceAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODELOCAL_ALLOW_COMMUNITY_WORKSPACE"))) {
	case "1", "true", "on", "yes", "enabled":
		return true
	default:
		return false
	}
}

// dashboardCommunityOptInEligible mirrors dashboardCommunityEligible but honors
// the workspace opt-in: workspace and image content may flow to community lanes
// while obvious secrets in the message or history stay blocked.
func dashboardCommunityOptInEligible(req dashboardChatRequest) bool {
	if dashboardLooksSensitive(req.Message) {
		return false
	}
	for _, item := range req.History {
		if dashboardLooksSensitive(item.Content) {
			return false
		}
	}
	return true
}

func dashboardTargetCoolingDown(target dashboardLLMTarget) bool {
	dashboardLLMHealth.Lock()
	defer dashboardLLMHealth.Unlock()
	until := dashboardLLMHealth.cooldownUntil[target.ID]
	if until.IsZero() || time.Now().After(until) {
		delete(dashboardLLMHealth.cooldownUntil, target.ID)
		return false
	}
	return true
}

func dashboardMarkTargetFailed(target dashboardLLMTarget) {
	dashboardLLMHealth.Lock()
	dashboardLLMHealth.cooldownUntil[target.ID] = time.Now().Add(30 * time.Second)
	dashboardLLMHealth.Unlock()
}

func dashboardMarkTargetHealthy(target dashboardLLMTarget) {
	dashboardLLMHealth.Lock()
	delete(dashboardLLMHealth.cooldownUntil, target.ID)
	dashboardLLMHealth.Unlock()
}

func dashboardRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *httpError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func dashboardLLMRetryDelay(attempt int) time.Duration {
	delay := 250 * time.Millisecond
	for i := 0; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func dashboardWaitLLMRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(dashboardLLMRetryDelay(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func dashboardValidateToolCalls(calls []llmToolCall) error {
	for _, call := range calls {
		arguments := strings.TrimSpace(call.Arguments)
		if strings.TrimSpace(call.Name) == "" || !strings.HasPrefix(arguments, "{") || !strings.HasSuffix(arguments, "}") {
			return errors.New("malformed tool call")
		}
	}
	return nil
}

func callDashboardLLMWithTools(selection string, allowCommunity bool, messages []map[string]any, tools []map[string]any) (dashboardLLMTarget, []llmToolCall, string, error) {
	return callDashboardLLMWithToolsContext(context.Background(), selection, allowCommunity, false, messages, tools)
}

func callDashboardLLMWithToolsContext(ctx context.Context, selection string, allowCommunity, allowModelFallback bool, messages []map[string]any, tools []map[string]any) (dashboardLLMTarget, []llmToolCall, string, error) {
	messages = dashboardWithSkillContext(messages)
	route := dashboardLLMRouteWithContext(ctx, selection, allowCommunity, allowModelFallback)
	if len(route) == 0 {
		return dashboardLLMTarget{}, nil, "", dashboardRouteError(selection, errors.New("no configured LLM route"))
	}
	var lastErr error
	for index, target := range route {
		if dashboardTargetCoolingDown(target) && index < len(route)-1 {
			continue
		}
		for attempt := 0; attempt < dashboardLLMRetryAttempts; attempt++ {
			if len(target.APIKeyBytes) > 0 {
				if err := dashboardProviderNetworkSafe(ctx, target.BaseURL); err != nil {
					lastErr = err
					break
				}
			}
			calls, content, err := callLLMWithToolsProtocol(dashboardTargetProtocol(target), target.BaseURL, dashboardTargetAPIKey(target), target.Model, messages, tools)
			if err == nil {
				err = dashboardValidateToolCalls(calls)
			}
			if err == nil {
				dashboardMarkTargetHealthy(target)
				return target, calls, content, nil
			}
			lastErr = err
			if !dashboardRetryableLLMError(err) || attempt == dashboardLLMRetryAttempts-1 {
				break
			}
			if waitErr := dashboardWaitLLMRetry(ctx, attempt); waitErr != nil {
				return target, nil, "", waitErr
			}
		}
		dashboardMarkTargetFailed(target)
	}
	return dashboardLLMTarget{}, nil, "", dashboardRouteError(selection, lastErr)
}

type dashboardCountingWriter struct {
	http.ResponseWriter
	written int
}

func (w *dashboardCountingWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.written += n
	return n, err
}

type dashboardLLMExecutionRoute struct {
	Selection          string
	AllowCommunity     bool
	AllowModelFallback bool
}

type dashboardLLMExecutionRouteKey struct{}

func dashboardWithLLMExecutionRoute(r *http.Request, selection string, allowCommunity, allowModelFallback bool) *http.Request {
	state := dashboardLLMExecutionRoute{
		Selection:          dashboardNormalizeModelSelection(selection),
		AllowCommunity:     allowCommunity,
		AllowModelFallback: allowModelFallback,
	}
	return r.WithContext(context.WithValue(r.Context(), dashboardLLMExecutionRouteKey{}, state))
}

func dashboardLLMExecutionRouteFromRequest(r *http.Request) (dashboardLLMExecutionRoute, bool) {
	if r == nil {
		return dashboardLLMExecutionRoute{}, false
	}
	state, ok := r.Context().Value(dashboardLLMExecutionRouteKey{}).(dashboardLLMExecutionRoute)
	return state, ok
}

func dashboardRecoverAgentRound(r *http.Request, messages []map[string]any, tools []map[string]any) (dashboardLLMTarget, []llmToolCall, string, error, bool) {
	state, ok := dashboardLLMExecutionRouteFromRequest(r)
	if !ok {
		return dashboardLLMTarget{}, nil, "", nil, false
	}
	target, calls, content, err := callDashboardLLMWithToolsContext(r.Context(), state.Selection, state.AllowCommunity, state.AllowModelFallback, messages, tools)
	return target, calls, content, err, true
}

func proxyDashboardLLMRouteStream(w http.ResponseWriter, flusher http.Flusher, selection string, allowCommunity bool, messages []map[string]any, tools []map[string]any, r *http.Request, s *Server, userID string) (dashboardLLMTarget, error) {
	skillPlan := dashboardSkillPlanForUser(r.Context(), s, userID, messages)
	if value := dashboardSkillHeaderValue(skillPlan); value != "" {
		w.Header().Set(dashboardSkillHeader, value)
	} else {
		w.Header().Del(dashboardSkillHeader)
	}
	r = r.WithContext(cloud.WithDashboardChatSkills(r.Context(), dashboardSkillMetadata(skillPlan)))
	messages = dashboardWithSkillPlan(messages, skillPlan)
	allowModelFallback := dashboardChatModeFromRequest(r) == "agent"
	r = dashboardWithLLMExecutionRoute(r, selection, allowCommunity, allowModelFallback)
	route := dashboardLLMRouteWithContext(r.Context(), selection, allowCommunity, allowModelFallback)
	if shadow := observeDashboardModelDecision(r.Context(), s, userID, route); shadow != nil {
		w.Header().Set("X-CodeLocal-Decision-Mode", string(shadow.Mode))
		w.Header().Set("X-CodeLocal-Decision-Status", shadow.Status)
		if shadow.Candidate != "" {
			w.Header().Set("X-CodeLocal-Decision-Model", shadow.Candidate)
		}
	}
	if len(route) == 0 {
		return dashboardLLMTarget{}, dashboardRouteError(selection, errors.New("no configured LLM route"))
	}
	var lastErr error
	for index, target := range route {
		if dashboardTargetCoolingDown(target) && index < len(route)-1 {
			continue
		}
		for attempt := 0; attempt < dashboardLLMRetryAttempts; attempt++ {
			if len(target.APIKeyBytes) > 0 {
				if err := dashboardProviderNetworkSafe(r.Context(), target.BaseURL); err != nil {
					lastErr = err
					break
				}
			}
			tracked := &dashboardCountingWriter{ResponseWriter: w}
			err := proxyLLMStreamProtocol(dashboardTargetProtocol(target), tracked, flusher, target.BaseURL, dashboardTargetAPIKey(target), target.Model, messages, tools, r, s, userID)
			if err == nil {
				dashboardMarkTargetHealthy(target)
				return target, nil
			}
			lastErr = err
			if tracked.written > 0 && !dashboardSafeToReroute(err) {
				return target, err
			}
			if !dashboardRetryableLLMError(err) || attempt == dashboardLLMRetryAttempts-1 {
				break
			}
			timer := time.NewTimer(dashboardLLMRetryDelay(attempt))
			select {
			case <-r.Context().Done():
				timer.Stop()
				return target, r.Context().Err()
			case <-timer.C:
			}
		}
		dashboardMarkTargetFailed(target)
	}
	return dashboardLLMTarget{}, dashboardRouteError(selection, lastErr)
}
