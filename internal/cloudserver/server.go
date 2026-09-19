package cloudserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
	"github.com/0xmarkhydra/codelocal/internal/cloudmcp"
	"github.com/0xmarkhydra/codelocal/internal/deviceauth"
	"github.com/0xmarkhydra/codelocal/internal/gateway"
	"github.com/0xmarkhydra/codelocal/internal/learnedskills"
	"github.com/0xmarkhydra/codelocal/internal/mcpgateway"
	"github.com/0xmarkhydra/codelocal/internal/memory"
	"github.com/0xmarkhydra/codelocal/internal/oauth"
	"github.com/0xmarkhydra/codelocal/internal/penpot"
	"github.com/0xmarkhydra/codelocal/internal/projectbrain"
	"github.com/0xmarkhydra/codelocal/internal/projectidentity"
	"github.com/0xmarkhydra/codelocal/internal/protocol"
	"github.com/0xmarkhydra/codelocal/internal/version"
	"github.com/0xmarkhydra/codelocal/internal/webauth"
	"github.com/0xmarkhydra/codelocal/internal/webutil"
)

type Server struct {
	Store       *cloud.Store
	Activation  *cloud.ActivationStore
	Hub         *gateway.Hub
	Coordinator *gateway.Coordinator
	Workspaces  *gateway.WorkspaceService
	WebAuth     *webauth.Manager
	OAuth       *oauth.Server
	Penpot      *penpot.Adapter
	MCP         *mcpgateway.Service
	Memory      *memory.Store
	Media       *s3MediaStore
	CloudMCP    *cloudmcp.Manager
	Mux         *http.ServeMux
	HTTP        *http.Server
	WebFrontend http.Handler
	InstanceID  string
	startedAt   time.Time
	requests    atomic.Uint64
}

func randomID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func projectBrainCloudSyncEnabled(userID, deviceID string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODELOCAL_PROJECT_BRAIN_CLOUD_SYNC"))) {
	case "0", "false", "off", "disabled":
		return false
	}
	// Rollout is opt-in and fail-closed. Deploying a new server binary without
	// configuring a cohort must never silently turn Project Brain on for 100%
	// of users.
	raw := strings.TrimSpace(os.Getenv("CODELOCAL_PROJECT_BRAIN_ROLLOUT_PERCENT"))
	if raw == "" {
		return false
	}
	percent, err := strconv.Atoi(raw)
	if err != nil || percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(userID) + "\x00" + strings.TrimSpace(deviceID)))
	bucket := int(binary.BigEndian.Uint16(digest[:2])) % 10000
	return bucket < percent*100
}

func New(ctx context.Context) (*Server, error) {
	base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	if base == "" {
		base = "http://localhost:" + defaultPort()
	}
	secret := os.Getenv("MCP_AUTH_SECRET")
	if secret == "" {
		return nil, errors.New("MCP_AUTH_SECRET is required")
	}
	webFrontend, err := newWebFrontendProxyFromEnv()
	if err != nil {
		return nil, err
	}

	store, err := cloud.New(ctx)
	if err != nil {
		return nil, err
	}
	activation := cloud.NewActivationStore(ctx, store.Redis)
	instanceID := os.Getenv("CODELOCAL_GATEWAY_INSTANCE_ID")
	if instanceID == "" {
		instanceID = first(os.Getenv("RAILWAY_REPLICA_ID"), os.Getenv("HOSTNAME"), randomID())
	}

	hub := gateway.NewHub(store, instanceID)
	coordinator := gateway.NewCoordinator(ctx, store.Redis, instanceID, func(callCtx context.Context, call gateway.RoutedCall) gateway.RoutedResult {
		return hub.HandleRouted(callCtx, call)
	}, func(userID, credentialID string) {
		hub.DisconnectCredential(userID, credentialID)
	})
	hub.SetCoordinator(coordinator)
	workspaceService := &gateway.WorkspaceService{Store: store, Activation: activation, Hub: hub, Coordinator: coordinator}
	auth := webauth.New(store, base)
	oauthServer, err := oauth.New(store, auth, base, secret)
	if err != nil {
		_ = activation.Close()
		_ = coordinator.Close()
		store.Close()
		return nil, err
	}
	var memoryStore *memory.Store
	memoryFlag := strings.ToLower(strings.TrimSpace(os.Getenv("CODELOCAL_MEMORY_ENABLED")))
	if memoryFlag != "0" && memoryFlag != "false" && memoryFlag != "off" {
		memoryStore = memory.NewStore(store.DB, memory.EmbedderFromEnv())
		probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
		_ = memoryStore.ProbeVector(probeCtx)
		probeCancel()

		graphFlag := strings.ToLower(strings.TrimSpace(os.Getenv("CODELOCAL_MEMORY_GRAPH_ENABLED")))
		graphEnabled := graphFlag != "0" && graphFlag != "false" && graphFlag != "off"
		memoryStore.SetGraphEnabled(graphEnabled)
		if graphEnabled {
			backfillCtx, backfillCancel := context.WithTimeout(ctx, 4*time.Second)
			if count, backfillErr := memoryStore.BackfillGraph(backfillCtx, 500); backfillErr != nil && backfillCtx.Err() == nil {
				slog.Warn("memory graph startup backfill failed; vector memory remains available", "error", backfillErr)
			} else if count > 0 {
				slog.Info("memory graph startup backfill projected existing memories", "count", count)
			}
			backfillCancel()
			go func() {
				backfillCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				total := 0
				for batch := 0; batch < 10 && backfillCtx.Err() == nil; batch++ {
					count, backfillErr := memoryStore.BackfillGraph(backfillCtx, 500)
					if backfillErr != nil {
						if backfillCtx.Err() == nil {
							slog.Warn("memory graph background backfill stopped; vector memory remains available", "error", backfillErr, "projected", total)
						}
						return
					}
					total += count
					if count < 500 {
						if total > 0 {
							slog.Info("memory graph background backfill complete", "projected", total)
						}
						return
					}
				}
			}()
		}
	}
	mediaStore, err := newS3MediaStoreFromEnvironment(ctx)
	if err != nil {
		_ = activation.Close()
		_ = coordinator.Close()
		store.Close()
		return nil, err
	}
	mcpService := mcpgateway.New(store, hub, workspaceService, memoryStore)

	s := &Server{
		Store:       store,
		Activation:  activation,
		Hub:         hub,
		Coordinator: coordinator,
		Workspaces:  workspaceService,
		WebAuth:     auth,
		OAuth:       oauthServer,
		MCP:         mcpService,
		Memory:      memoryStore,
		Media:       mediaStore,
		CloudMCP:    cloudmcp.NewManager(),
		Mux:         http.NewServeMux(),
		WebFrontend: webFrontend,
		InstanceID:  instanceID,
		startedAt:   time.Now(),
	}
	// Optional integration failure must not prevent the cloud/runtime starting.
	if os.Getenv("CODELOCAL_PENPOT_SSO_ONLY") == "true" {
		s.Penpot, err = penpot.New(oauthServer, os.Getenv("CODELOCAL_PENPOT_BACKEND_URL"),
			os.Getenv("CODELOCAL_PENPOT_MCP_URL"), os.Getenv("CODELOCAL_PENPOT_WS_URL"))
		if err != nil {
			slog.Warn("Penpot integration unavailable: invalid private upstream configuration")
		}
	}
	mcpService.SetBlogMediaImporter(s)
	s.routes()
	s.HTTP = &http.Server{
		Addr:              host() + ":" + defaultPort(),
		Handler:           s.middleware(s.webFrontendMiddleware(s.Mux)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	go hub.HeartbeatLoop(ctx, 20*time.Second, 70*time.Second)
	go s.forumEmailWorker(ctx)
	if mediaStore != nil {
		go mediaStore.cleanupLoop(ctx)
	}
	return s, nil
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultPort() string {
	if value := os.Getenv("PORT"); value != "" {
		return value
	}
	return "3333"
}

func host() string {
	if value := os.Getenv("HOST"); value != "" {
		return value
	}
	return "0.0.0.0"
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		s.requests.Add(1)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-CodeLocal-Gateway", s.InstanceID)
		mcpRPCMethod := ""
		if r.URL.Path == "/mcp" {
			mcpRPCMethod = mcpgateway.MCPRequestMethod(r)
		}
		next.ServeHTTP(w, r)
		if r.URL.Path != "/health" {
			fields := []any{"method", r.Method, "path", r.URL.Path, "durationMs", time.Since(started).Milliseconds(), "gateway", s.InstanceID}
			if r.URL.Path == "/mcp" {
				fields = append(fields,
					"mcpRpcMethod", mcpRPCMethod,
					"mcpProtocolVersion", strings.TrimSpace(r.Header.Get("Mcp-Protocol-Version")),
					"mcpTransport", strings.TrimSpace(w.Header().Get("X-CodeLocal-MCP-Transport")),
					"mcpSessionIdPresent", strings.TrimSpace(r.Header.Get("Mcp-Session-Id")) != "",
				)
			}
			slog.Info("http request", fields...)
		}
	})
}

func (s *Server) routes() {
	mux := s.Mux
	s.WebAuth.Register(mux)
	s.OAuth.Register(mux)
	mux.Handle("GET /api/status", s.WebAuth.Require(http.HandlerFunc(s.apiStatus)))
	mux.HandleFunc("GET /api/v1/dashboard/overview", s.dashboardOverviewAPI)
	mux.HandleFunc("GET /api/v1/dashboard/models", s.dashboardModelsAPI)
	mux.HandleFunc("GET /api/v1/dashboard/ai/providers", s.dashboardAIProvidersAPI)
	mux.HandleFunc("POST /api/v1/dashboard/ai/providers", s.dashboardAIProvidersAPI)
	mux.HandleFunc("PATCH /api/v1/dashboard/ai/providers/{id}", s.dashboardAIProviderAPI)
	mux.HandleFunc("DELETE /api/v1/dashboard/ai/providers/{id}", s.dashboardAIProviderAPI)
	mux.HandleFunc("POST /api/v1/dashboard/ai/providers/{id}/test", s.dashboardAIProviderTestAPI)
	mux.HandleFunc("POST /api/v1/dashboard/chat", s.dashboardChatAPI)
	mux.HandleFunc("POST /api/v1/dashboard/media/presign", s.dashboardMediaPresign)
	mux.HandleFunc("POST /api/v1/dashboard/media/upload", s.dashboardMediaUpload)
	mux.HandleFunc("GET /api/v1/dashboard/chat/history", s.dashboardChatHistoryAPI)
	mux.HandleFunc("DELETE /api/v1/dashboard/chat/history", s.dashboardChatHistoryAPI)
	mux.HandleFunc("GET /api/v1/dashboard/chat/threads", s.dashboardChatThreadsAPI)
	mux.HandleFunc("POST /api/v1/dashboard/chat/threads", s.dashboardChatThreadsAPI)
	mux.HandleFunc("GET /api/v1/dashboard/chat/threads/{id}", s.dashboardChatThreadAPI)
	mux.HandleFunc("PATCH /api/v1/dashboard/chat/threads/{id}", s.dashboardChatThreadAPI)
	mux.HandleFunc("DELETE /api/v1/dashboard/chat/threads/{id}", s.dashboardChatThreadAPI)
	s.registerSkillRoutes(mux)
	s.registerPluginRoutes(mux)
	mux.HandleFunc("GET /api/v1/devices", s.devicesResourceAPI)
	mux.HandleFunc("GET /api/v1/workspaces", s.workspacesResourceAPI)
	mux.HandleFunc("GET /api/v1/runtime/settings", s.runtimeSettingsResourceAPI)
	mux.HandleFunc("GET /api/v1/decision/status", s.decisionStatusAPI)
	mux.HandleFunc("POST /api/v1/runtime/settings/config", s.runtimeConfigMutationAPI)
	mux.HandleFunc("POST /api/v1/runtime/settings/execution", s.runtimeExecutionModeMutationAPI)
	mux.HandleFunc("POST /api/v1/runtime/settings/secret", s.runtimeSecretMutationAPI)
	mux.HandleFunc("GET /api/v1/usage", s.usageResourceAPI)
	mux.HandleFunc("GET /api/v1/knowledge/graph", s.knowledgeGraphResourceAPI)
	mux.HandleFunc("GET /api/v1/knowledge/health", s.knowledgeHealthResourceAPI)
	mux.HandleFunc("GET /api/v1/code/graph", s.codeGraphResourceAPI)
	mux.HandleFunc("GET /api/v1/account", s.accountResourceAPI)
	mux.HandleFunc("GET /api/v1/auth/csrf", s.authCSRFResourceAPI)
	mux.HandleFunc("GET /api/v1/invite", s.inviteResourceAPI)
	mux.HandleFunc("GET /api/v1/admin", s.adminResourceAPI)
	mux.HandleFunc("GET /api/v1/pair/approve", s.pairApproveResourceAPI)
	mux.HandleFunc("POST /api/v1/devices/{deviceID}/revoke", s.revokeDeviceResourceAPI)
	mux.HandleFunc("POST /api/v1/workspaces/{deviceID}/{workspaceID}/remove", s.removeWorkspaceResourceAPI)
	mux.HandleFunc("POST /api/v1/system-apps/{appID}/{deviceID}/install", s.installSystemAppResourceAPI)
	mux.Handle("GET /api/collective/preferences", s.WebAuth.Require(http.HandlerFunc(s.collectivePreferencesGet)))
	mux.Handle("POST /api/collective/preferences", s.WebAuth.Require(http.HandlerFunc(s.collectivePreferencesPost)))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /internal/web-edge-probe", s.webEdgeProbe)
	mux.HandleFunc("GET /.well-known/openai-apps-challenge", s.openAIAppsChallenge)
	mux.HandleFunc("GET /assets/{name}", s.asset)
	mux.Handle("POST /pair/approve", s.WebAuth.Require(http.HandlerFunc(s.pairApprovePost)))

	var pairStart http.Handler = http.HandlerFunc(s.pairStart)
	pairStart = webutil.RateLimit(s.Store, webutil.RateLimitOptions{
		Scope:  "pair-start-device",
		Limit:  12,
		Window: 10 * time.Minute,
		Subject: func(r *http.Request) string {
			var body struct {
				DeviceID string `json:"deviceId"`
			}
			raw, _ := readBodyReplay(r, 64<<10)
			_ = json.Unmarshal(raw, &body)
			return body.DeviceID
		},
	}, pairStart)
	mux.Handle("POST /pair/start", webutil.RateLimit(s.Store, webutil.RateLimitOptions{Scope: "pair-start-ip", Limit: 40, Window: 10 * time.Minute}, pairStart))

	var claim http.Handler = http.HandlerFunc(s.pairClaim)
	mux.Handle("POST /pair/claim", webutil.RateLimit(s.Store, webutil.RateLimitOptions{Scope: "pair-claim-ip", Limit: 300, Window: time.Minute}, claim))
	mux.HandleFunc("POST /api/client/auth/check", s.clientAuthCheck)
	mux.HandleFunc("POST /api/client/auth/logout", s.clientAuthLogout)
	mediaPresign := webutil.RateLimit(s.Store, webutil.RateLimitOptions{
		Scope: "media-presign-device", Limit: 240, Window: time.Minute,
		Subject: func(r *http.Request) string { id, _ := deviceAuth(r); return id },
	}, http.HandlerFunc(s.mediaPresign))
	mux.Handle("POST /api/client/media/presign", webutil.RateLimit(s.Store, webutil.RateLimitOptions{Scope: "media-presign-ip", Limit: 600, Window: time.Minute}, mediaPresign))
	mux.Handle("POST /api/client/artifacts/presign", videoArtifactPresignHandler(s))
	mux.HandleFunc("GET /api/public/artifacts/{owner}/{file}", s.publicVideoArtifact)
	mux.HandleFunc("POST /api/client/workspaces/sync", s.workspaceSync)
	mux.HandleFunc("POST /api/client/mcp/status", s.mcpStatusSync)
	mux.HandleFunc("POST /api/client/knowledge/sync", s.knowledgeSync)
	mux.HandleFunc("POST /api/client/runtime/poll", s.runtimePoll)
	mux.HandleFunc("POST /api/client/runtime/revocation-ack", s.revocationAck)
	mux.Handle("/client", s.Hub)
	mcpProtected := s.OAuth.RequireMCP(s.MCP.Handler())
	mcpAuthChallenge := mcpgateway.MCPAuthChallengeHandler(s.OAuth.BaseURL + "/.well-known/oauth-protected-resource")
	mux.Handle("/mcp", mcpgateway.PublicDiscoveryOrProtected(s.MCP.PublicDiscoveryHandler(), mcpAuthChallenge, mcpProtected))
	mux.HandleFunc("/api/v1/penpot/mcp", s.Penpot.ServeMCP)
	mux.HandleFunc("/api/v1/penpot/ws", s.Penpot.ServeWS)

	if os.Getenv("CODELOCAL_ENABLE_PPROF") == "1" {
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	}
}

func readBodyReplay(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return raw, nil
}

func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	allowed := map[string]string{
		"chatgpt-plugin-icon.png": "image/png",
		"codelocal-icon.png":      "image/png",
		"claude.svg":              "image/svg+xml",
		"moonshotai.svg":          "image/svg+xml",
		"deepseek.svg":            "image/svg+xml",
		"apple-touch-icon.png":    "image/png",
		"favicon.ico":             "image/x-icon",
	}
	contentType := allowed[name]
	if contentType == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(filepath.Join("assets", name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public,max-age=86400")
	_, _ = w.Write(data)
}

func (s *Server) identity(r *http.Request) (*webauth.Identity, bool) {
	identity, _ := s.WebAuth.Identity(r)
	return identity, identity != nil
}

func schemaMigrationPayload(status cloud.SchemaMigrationStatus, err error) map[string]any {
	payload := map[string]any{
		"available":            err == nil,
		"currentVersion":       status.CurrentVersion,
		"targetVersion":        status.TargetVersion,
		"appliedCount":         status.AppliedCount,
		"upToDate":             err == nil && status.UpToDate,
		"projectBrainPlanHash": status.ProjectBrainPlanHash,
	}
	if status.TargetVersion == 0 {
		payload["targetVersion"] = cloud.LatestSchemaMigrationVersion()
	}
	if status.ProjectBrainPlanHash == "" {
		payload["projectBrainPlanHash"] = cloud.ProjectBrainMigrationPlanHash()
	}
	return payload
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.identity(r)
	if !ok {
		webutil.JSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
		return
	}
	workspaces, _ := s.Workspaces.Catalog(r.Context(), identity.User.ID)
	devices, _ := s.Store.ListDevices(r.Context(), identity.User.ID)
	usage24h, _ := s.Store.MCPUsageSummary(r.Context(), identity.User.ID, time.Now().Add(-24*time.Hour).UnixMilli())
	usage30d, _ := s.Store.MCPUsageSummary(r.Context(), identity.User.ID, time.Now().Add(-30*24*time.Hour).UnixMilli())
	usageAll, _ := s.Store.MCPUsageSummary(r.Context(), identity.User.ID, 0)
	schemaStatus, schemaErr := s.Store.SchemaMigrationStatus(r.Context())
	durableLearning, _ := s.Store.DurableOutboxHealth(r.Context(), identity.User.ID)
	_, canonicalGraphFreshness, _ := s.Store.CanonicalGraphFreshness(r.Context(), identity.User.ID)
	_, canonicalEmbeddingFreshness, _ := s.Store.CanonicalEmbeddingFreshness(r.Context(), identity.User.ID)
	semanticCanary, _ := s.Store.CanonicalSemanticCanaryMetrics(r.Context(), identity.User.ID)
	for i := range devices {
		devices[i].SecretHash = ""
	}
	webutil.JSON(w, http.StatusOK, map[string]any{
		"user":                        map[string]any{"id": identity.User.ID, "email": identity.User.Email},
		"workspaces":                  workspaces,
		"devices":                     devices,
		"gateway":                     s.InstanceID,
		"schemaMigration":             schemaMigrationPayload(schemaStatus, schemaErr),
		"durableLearning":             durableLearning,
		"canonicalGraphFreshness":     canonicalGraphFreshness,
		"canonicalEmbeddingFreshness": canonicalEmbeddingFreshness,
		"canonicalSemanticCanary":     semanticCanary,
		"mcpTokenUsage": map[string]any{
			"estimated": true,
			"scope":     "MCP payload only; not full AI model/provider billing tokens",
			"last24h":   usage24h,
			"last30d":   usage30d,
			"allTime":   usageAll,
		},
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	schemaCtx, schemaCancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	schemaStatus, schemaErr := s.Store.SchemaMigrationStatus(schemaCtx)
	schemaCancel()
	outboxCtx, outboxCancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	durableLearning, _ := s.Store.DurableOutboxHealth(outboxCtx, "")
	outboxCancel()
	graphCtx, graphCancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	_, canonicalGraphFreshness, _ := s.Store.CanonicalGraphFreshness(graphCtx, "")
	graphCancel()
	embeddingCtx, embeddingCancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	_, canonicalEmbeddingFreshness, _ := s.Store.CanonicalEmbeddingFreshness(embeddingCtx, "")
	embeddingCancel()
	canaryCtx, canaryCancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	semanticCanary, _ := s.Store.CanonicalSemanticCanaryMetrics(canaryCtx, "")
	canaryCancel()
	agentMemory := map[string]any{"enabled": s.Memory != nil}
	visualMedia := map[string]any{"configured": s.Media != nil}
	if s.Media != nil {
		visualMedia["urlTTLSeconds"] = int64(s.Media.urlTTL.Seconds())
		visualMedia["retentionSeconds"] = int64(s.Media.retention.Seconds())
		visualMedia["maxBytes"] = s.Media.maxBytes
	}
	if s.Memory != nil {
		agentMemory["vectorAvailable"] = s.Memory.VectorAvailable()
		agentMemory["vectorDimension"] = s.Memory.VectorDimension()
		agentMemory["embeddingProvider"] = s.Memory.EmbeddingProvider()
		agentMemory["embeddingModel"] = s.Memory.EmbeddingModel()
		agentMemory["graphEnabled"] = s.Memory.GraphEnabled()
	}
	webutil.JSON(w, http.StatusOK, map[string]any{
		"ok":                    true,
		"version":               version.Version,
		"protocolVersion":       protocol.Version,
		"goVersion":             runtime.Version(),
		"gateway":               s.InstanceID,
		"uptimeSeconds":         int64(time.Since(s.startedAt).Seconds()),
		"onlineLocalWorkspaces": len(s.Hub.LocalClients("")),
		"requests":              s.requests.Load(),
		"memory": map[string]any{
			"allocBytes":     mem.Alloc,
			"heapInUseBytes": mem.HeapInuse,
			"sysBytes":       mem.Sys,
			"gcCycles":       mem.NumGC,
		},
		"goroutines":                  runtime.NumGoroutine(),
		"agentMemory":                 agentMemory,
		"visualMedia":                 visualMedia,
		"schemaMigration":             schemaMigrationPayload(schemaStatus, schemaErr),
		"durableLearning":             durableLearning,
		"canonicalGraphFreshness":     canonicalGraphFreshness,
		"canonicalEmbeddingFreshness": canonicalEmbeddingFreshness,
		"canonicalSemanticCanary":     semanticCanary,
		"toolSurface":                 s.MCP.ToolSurface(),
	})
}

func (s *Server) pairStart(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DeviceID   string `json:"deviceId"`
		DeviceName string `json:"deviceName"`
	}
	if err := webutil.DecodeJSON(r, 64<<10, &input); err != nil || strings.TrimSpace(input.DeviceID) == "" || len(input.DeviceID) > 200 {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "deviceId_required"})
		return
	}
	if input.DeviceName == "" {
		input.DeviceName = input.DeviceID
	}
	if len(input.DeviceName) > 120 {
		input.DeviceName = input.DeviceName[:120]
	}
	pairing, err := s.Store.CreatePairing(r.Context(), input.DeviceID, input.DeviceName, 10*time.Minute)
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": "pairing_failed"})
		return
	}
	base := strings.TrimRight(s.WebAuth.PublicBaseURL, "/")
	webutil.JSON(w, http.StatusOK, map[string]any{
		"pairingId":      pairing.PairingID,
		"code":           pairing.Code,
		"expiresAt":      pairing.ExpiresAt,
		"approveUrl":     base + "/pair/approve?pairingId=" + url.QueryEscape(pairing.PairingID),
		"retrySafeClaim": true,
		"deviceSigning":  true,
	})
}

func (s *Server) pairApprovePost(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.identity(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	pairingID := r.FormValue("pairingId")
	next := "/pair/approve?pairingId=" + url.QueryEscape(pairingID)
	if !s.WebAuth.RequireFreshSecurityContext(w, r, identity, next) {
		return
	}
	if !s.WebAuth.VerifyCSRF(r) {
		http.Redirect(w, r, next+"&error="+url.QueryEscape("Invalid security token. Please try again."), http.StatusSeeOther)
		return
	}
	pairing, err := s.Store.ApprovePairing(r.Context(), pairingID, r.FormValue("code"), identity.User.ID)
	if err != nil || pairing == nil {
		http.Redirect(w, r, next+"&error="+url.QueryEscape("Invalid or expired pairing request/code."), http.StatusSeeOther)
		return
	}
	s.Store.Audit(cloud.AuditEvent{UserID: identity.User.ID, Event: "device.pairing_approved", DeviceID: pairing.DeviceID, Detail: map[string]any{"deviceName": pairing.DeviceName}})
	http.Redirect(w, r, next+"&approved=1", http.StatusSeeOther)
}

func (s *Server) disconnectCredentialEverywhere(userID, credentialID string) {
	if userID == "" || credentialID == "" {
		return
	}
	s.Hub.DisconnectCredential(userID, credentialID)
	if s.Coordinator != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.Coordinator.BroadcastCredentialDisconnect(ctx, userID, credentialID); err != nil {
			slog.Warn("credential disconnect broadcast failed", "error", err, "credentialId", credentialID)
		}
	}
}

func (s *Server) pairClaim(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PairingID            string `json:"pairingId"`
		Code                 string `json:"code"`
		CredentialID         string `json:"credentialId"`
		CredentialSecretHash string `json:"credentialSecretHash"`
		DevicePublicKey      string `json:"devicePublicKey"`
	}
	if webutil.DecodeJSON(r, 64<<10, &input) != nil {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	credentialID := strings.TrimSpace(input.CredentialID)
	secretHash := strings.TrimSpace(input.CredentialSecretHash)
	publicKey := strings.TrimSpace(input.DevicePublicKey)
	secret := ""
	if (credentialID == "") != (secretHash == "") {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_credential_claim"})
		return
	}
	if credentialID == "" {
		// Backward compatibility for clients that predate retry-safe claims.
		credentialID = "cld_" + randomID()
		secret = cloud.RandomHex(40)
		secretHash = cloud.HashSecret(secret)
	} else {
		decodedHash, hashErr := hex.DecodeString(secretHash)
		if !strings.HasPrefix(credentialID, "cld_") || len(credentialID) > 128 || hashErr != nil || len(decodedHash) != 32 {
			webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_credential_claim"})
			return
		}
	}
	if publicKey != "" && !deviceauth.ValidPublicKey(publicKey) {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_device_public_key"})
		return
	}
	device, err := s.Store.ClaimPairing(r.Context(), input.PairingID, input.Code, credentialID, secretHash, publicKey)
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": "pairing_failed"})
		return
	}
	if device == nil {
		webutil.JSON(w, http.StatusConflict, map[string]any{"error": "pairing_not_approved_or_expired"})
		return
	}
	if device.PreviousCredentialID != "" && device.PreviousCredentialID != credentialID {
		s.disconnectCredentialEverywhere(device.UserID, device.PreviousCredentialID)
	}
	s.Store.Audit(cloud.AuditEvent{UserID: device.UserID, Event: "device.paired", DeviceID: device.DeviceID, Detail: map[string]any{"credentialId": credentialID, "deviceName": device.DeviceName}})
	output := map[string]any{
		"credentialId": credentialID,
		"deviceId":     device.DeviceID,
		"deviceName":   device.DeviceName,
	}
	if user, userErr := s.Store.UserByID(r.Context(), device.UserID); userErr == nil && user != nil {
		output["email"] = user.Email
	}
	if secret != "" {
		output["credentialSecret"] = secret
	}
	webutil.JSON(w, http.StatusOK, output)
}

func deviceAuth(r *http.Request) (string, string) {
	id := r.Header.Get("X-CodeLocal-Credential-Id")
	auth := r.Header.Get("Authorization")
	secret := ""
	if strings.HasPrefix(auth, "Device ") {
		secret = strings.TrimPrefix(auth, "Device ")
	}
	return id, secret
}

func (s *Server) authenticateDevice(r *http.Request) (*cloud.Device, error) {
	id, secret := deviceAuth(r)
	if id == "" || secret == "" {
		return nil, nil
	}
	device, err := s.Store.AuthenticateDevice(r.Context(), id, cloud.HashSecret(secret))
	if err != nil || device == nil {
		return device, err
	}
	if err := s.verifySignedDeviceRequest(r, device); err != nil {
		return device, &deviceProofError{err: err}
	}
	return device, nil
}

const credentialCheckSemantics = "credential-v1"

func (s *Server) clientAuthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-CodeLocal-Auth-Check", credentialCheckSemantics)
	id, secret := deviceAuth(r)
	if id == "" || secret == "" {
		webutil.JSON(w, http.StatusUnauthorized, map[string]any{"error": "credential_invalid"})
		return
	}
	device, err := s.Store.AuthenticateDevice(r.Context(), id, cloud.HashSecret(secret))
	if err != nil || device == nil {
		webutil.JSON(w, http.StatusUnauthorized, map[string]any{"error": "credential_invalid"})
		return
	}
	output := map[string]any{"ok": true, "deviceId": device.DeviceID, "now": time.Now().UnixMilli()}
	if user, userErr := s.Store.UserByID(r.Context(), device.UserID); userErr == nil && user != nil {
		output["email"] = user.Email
	}
	webutil.JSON(w, http.StatusOK, output)
}

func (s *Server) clientAuthLogout(w http.ResponseWriter, r *http.Request) {
	device, err := s.authenticateDevice(r)
	if s.writeDeviceAuthFailure(w, device, err) {
		return
	}
	credentialID, _ := deviceAuth(r)
	revoked, err := s.Store.RevokeDevice(r.Context(), device.UserID, credentialID)
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": "device_logout_failed"})
		return
	}
	s.disconnectCredentialEverywhere(device.UserID, credentialID)
	presenceCtx, cancelPresence := context.WithTimeout(context.Background(), 2*time.Second)
	_ = s.Activation.ClearPresence(presenceCtx, device.UserID, device.DeviceID)
	cancelPresence()
	s.Store.Audit(cloud.AuditEvent{UserID: device.UserID, Event: "device.logout", DeviceID: device.DeviceID, Detail: map[string]any{"credentialId": credentialID}})
	webutil.JSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": revoked})
}

func (s *Server) knowledgeSync(w http.ResponseWriter, r *http.Request) {
	device, err := s.authenticateDevice(r)
	if s.writeDeviceAuthFailure(w, device, err) {
		return
	}
	if !projectBrainCloudSyncEnabled(device.UserID, device.DeviceID) {
		webutil.JSON(w, http.StatusOK, cloud.KnowledgeManifestSyncResult{Disabled: true, ActiveRevisions: map[string]string{}})
		return
	}
	var input struct {
		WorkspaceID     string                     `json:"workspaceId"`
		ProjectIdentity projectidentity.Snapshot   `json:"projectIdentity"`
		Delta           projectbrain.ManifestDelta `json:"delta"`
	}
	if webutil.DecodeJSON(r, 320<<10, &input) != nil {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`).MatchString(input.WorkspaceID) {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_workspace"})
		return
	}
	binding, err := s.Store.ResolveWorkspaceProject(r.Context(), device.UserID, device.DeviceID, input.WorkspaceID, input.ProjectIdentity)
	if err != nil || binding.ProjectID == "" {
		slog.Warn("project brain binding refresh failed; background sync will retry", "workspaceId", input.WorkspaceID, "error", err)
		webutil.JSON(w, http.StatusServiceUnavailable, map[string]any{"error": "knowledge_binding_unavailable"})
		return
	}
	canonicalRepositories := cloud.CanonicalizeProjectRepositories(input.ProjectIdentity.Repositories, binding.RepositoryIDMap)
	result, err := s.Store.SyncKnowledgeDelta(r.Context(), device.UserID, device.DeviceID, input.WorkspaceID, binding.ProjectID, canonicalRepositories, input.Delta)
	if err != nil {
		slog.Warn("project brain delta sync failed; runtime remains usable", "workspaceId", input.WorkspaceID, "projectId", binding.ProjectID, "error", err)
		webutil.JSON(w, http.StatusServiceUnavailable, map[string]any{"error": "knowledge_sync_failed"})
		return
	}
	webutil.JSON(w, http.StatusOK, result)
}

func (s *Server) workspaceSync(w http.ResponseWriter, r *http.Request) {
	device, err := s.authenticateDevice(r)
	if s.writeDeviceAuthFailure(w, device, err) {
		return
	}
	brainEnabled := projectBrainCloudSyncEnabled(device.UserID, device.DeviceID)
	var input struct {
		ClientVersion string `json:"clientVersion"`
		Workspaces    []struct {
			WorkspaceID       string                       `json:"workspaceId"`
			WorkspaceName     string                       `json:"workspaceName"`
			System            bool                         `json:"system,omitempty"`
			SystemApp         bool                         `json:"systemApp,omitempty"`
			Managed           bool                         `json:"managed,omitempty"`
			Hidden            bool                         `json:"hidden,omitempty"`
			ProjectIdentity   projectidentity.Snapshot     `json:"projectIdentity"`
			LearnedSkills     []cloud.LearnedSkillMetadata `json:"learnedSkills"`
			KnowledgeManifest *projectbrain.Manifest       `json:"knowledgeManifest,omitempty"`
		} `json:"workspaces"`
	}
	if webutil.DecodeJSON(r, 1<<20, &input) != nil {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if len(input.Workspaces) > 500 {
		input.Workspaces = input.Workspaces[:500]
	}

	ids := make([]string, 0, len(input.Workspaces))
	synced := 0
	knowledgeResults := map[string]cloud.KnowledgeManifestSyncResult{}
	portableSkillResults := map[string][]learnedskills.PortableRecipe{}
	runtimeSettings := map[string]cloud.RuntimeMaterializedConfig{}
	deviceMCPServers, mcpSettingsErr := s.Store.MaterializeLocalMCPConnections(r.Context(), device.UserID, device.DeviceID)
	if mcpSettingsErr != nil {
		slog.Warn("local MCP desired state lookup failed; workspace sync remains usable", "deviceId", device.DeviceID, "error", mcpSettingsErr)
		deviceMCPServers = nil
	}
	validID := regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
	for _, item := range input.Workspaces {
		if !validID.MatchString(item.WorkspaceID) {
			continue
		}
		name := strings.TrimSpace(item.WorkspaceName)
		if name == "" {
			name = item.WorkspaceID
		}
		if len(name) > 120 {
			name = name[:120]
		}
		// Legacy runtimes did not send lifecycle metadata for Video Studio.
		// Normalize only its exact historical ID, never a generic system-* prefix.
		if item.WorkspaceID == cloud.OpenMontageWorkspaceID {
			name = cloud.OpenMontageName
			item.System, item.SystemApp, item.Managed, item.Hidden = true, true, true, false
		}
		ids = append(ids, item.WorkspaceID)
		key := gateway.ClientKey(device.UserID, device.DeviceID, item.WorkspaceID)
		owner, _ := s.Coordinator.Owner(r.Context(), key)
		if owner == "" {
			caps := map[string]any{"authorized": true, "sleeping": true, "system": item.System, "systemApp": item.SystemApp, "managed": item.Managed, "hidden": item.Hidden}
			if input.ClientVersion != "" {
				caps["clientVersion"] = truncate(input.ClientVersion, 80)
			}
			if err := s.Store.UpsertWorkspace(r.Context(), cloud.Workspace{
				UserID:          device.UserID,
				DeviceID:        device.DeviceID,
				WorkspaceID:     item.WorkspaceID,
				WorkspaceName:   name,
				ProtocolVersion: protocol.Version,
				Capabilities:    caps,
			}); err != nil {
				slog.Warn("workspace sync persistence failed", "workspaceId", item.WorkspaceID, "error", err)
				continue
			}
		}
		binding, bindingErr := s.Store.ResolveWorkspaceProject(r.Context(), device.UserID, device.DeviceID, item.WorkspaceID, item.ProjectIdentity)
		if bindingErr != nil {
			slog.Warn("workspace project identity resolution failed; workspace remains usable", "workspaceId", item.WorkspaceID, "error", bindingErr)
		}
		if brainEnabled && bindingErr == nil && binding.ProjectID != "" && item.KnowledgeManifest != nil {
			canonicalRepositories := cloud.CanonicalizeProjectRepositories(item.ProjectIdentity.Repositories, binding.RepositoryIDMap)
			result, knowledgeErr := s.Store.SyncKnowledgeManifest(r.Context(), device.UserID, device.DeviceID, item.WorkspaceID, binding.ProjectID, canonicalRepositories, *item.KnowledgeManifest)
			if knowledgeErr != nil {
				// Knowledge is an enrichment layer. Legacy clients may still attach a
				// full manifest to workspace sync, but a transient Project Brain/DB
				// failure must never make the runtime itself unavailable.
				slog.Warn("project knowledge manifest sync failed; workspace remains usable", "workspaceId", item.WorkspaceID, "projectId", binding.ProjectID, "error", knowledgeErr)
			} else {
				knowledgeResults[item.WorkspaceID] = result
			}
		}
		projectID := ""
		if bindingErr == nil {
			projectID = binding.ProjectID
		}
		if err := s.Store.SyncLearnedSkillMetadata(r.Context(), device.UserID, device.DeviceID, item.WorkspaceID, projectID, item.LearnedSkills); err != nil {
			slog.Warn("learned skill metadata sync failed; local skills remain authoritative", "workspaceId", item.WorkspaceID, "error", err)
		}
		if projectID != "" {
			portable, portableErr := s.Store.ProjectPortableLearnedSkills(r.Context(), device.UserID, projectID, 32)
			if portableErr != nil {
				slog.Warn("portable learned skill lookup failed; workspace remains usable", "workspaceId", item.WorkspaceID, "projectId", projectID, "error", portableErr)
			} else if len(portable) > 0 {
				portableSkillResults[item.WorkspaceID] = portable
			}
		}
		snapshot, settingsErr := s.Store.ResolveRuntimeConfig(r.Context(), device.UserID, device.DeviceID, item.WorkspaceID)
		if settingsErr != nil {
			slog.Warn("runtime config lookup failed; workspace remains usable", "workspaceId", item.WorkspaceID, "error", settingsErr)
		} else {
			secrets, secretErr := s.Store.MaterializeRuntimeSecrets(r.Context(), device.UserID, device.DeviceID, item.WorkspaceID)
			if secretErr != nil {
				slog.Warn("runtime secret materialization failed; config remains usable", "workspaceId", item.WorkspaceID, "error", secretErr)
				secrets = map[string]string{}
			}
			s.materializePenpotGrant(r.Context(), device.UserID, device.DeviceID, item.WorkspaceID, secrets)
			runtimeSettings[item.WorkspaceID] = cloud.RuntimeMaterializedConfig{Snapshot: snapshot, Secrets: secrets, MCPServers: deviceMCPServers}
		}
		synced++
	}

	removed, err := s.Store.ReconcileWorkspaces(r.Context(), device.UserID, device.DeviceID, ids)
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": "workspace_sync_failed"})
		return
	}
	// Older runtimes acknowledge a revoke by removing the workspace locally and
	// syncing the reduced registry. Convert that sync into the same acknowledgement
	// event used by newer runtimes so users do not see a false timeout.
	for _, workspaceID := range removed {
		_, _ = s.Activation.AcknowledgeRevocation(r.Context(), device.UserID, device.DeviceID, workspaceID, "")
	}
	_ = s.Activation.Heartbeat(r.Context(), device.UserID, device.DeviceID, ids, 45*time.Second)
	s.Store.Audit(cloud.AuditEvent{UserID: device.UserID, Event: "runtime.workspaces_synced", DeviceID: device.DeviceID, Detail: map[string]any{"count": synced, "removed": len(removed)}})
	webutil.JSON(w, http.StatusOK, map[string]any{
		"synced": synced, "removed": len(removed), "knowledge": knowledgeResults, "portableSkills": portableSkillResults, "runtimeSettings": runtimeSettings, "syncedAt": time.Now().UnixMilli(),
		"projectBrain": map[string]any{"cloudSyncEnabled": brainEnabled},
	})
}

func truncate(value string, n int) string {
	value = strings.TrimSpace(value)
	if len(value) > n {
		return value[:n]
	}
	return value
}

func (s *Server) runtimePoll(w http.ResponseWriter, r *http.Request) {
	device, err := s.authenticateDevice(r)
	if s.writeDeviceAuthFailure(w, device, err) {
		return
	}
	var input struct {
		WorkspaceIDs []string `json:"workspaceIds"`
		WaitMS       int      `json:"waitMs"`
	}
	if webutil.DecodeJSON(r, 1<<20, &input) != nil {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if len(input.WorkspaceIDs) > 500 {
		input.WorkspaceIDs = input.WorkspaceIDs[:500]
	}
	wait := time.Duration(input.WaitMS) * time.Millisecond
	if wait < 0 {
		wait = 0
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	ttl := wait + 15*time.Second
	if ttl < 20*time.Second {
		ttl = 20 * time.Second
	}
	_ = s.Activation.Heartbeat(r.Context(), device.UserID, device.DeviceID, input.WorkspaceIDs, ttl)
	activation, revocation, err := s.Activation.WaitForNext(r.Context(), device.UserID, device.DeviceID, wait)
	if err != nil && r.Context().Err() == nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": "runtime_poll_failed"})
		return
	}
	if activation != nil {
		authorized, _ := s.Activation.IsAuthorized(r.Context(), device.UserID, device.DeviceID, activation.WorkspaceID)
		if !authorized {
			s.Store.Audit(cloud.AuditEvent{UserID: device.UserID, Event: "workspace.activation_rejected", DeviceID: device.DeviceID, WorkspaceID: activation.WorkspaceID, Detail: map[string]any{"requestId": activation.RequestID, "reason": "not-authorized"}})
			activation = nil
		}
	}
	webutil.JSON(w, http.StatusOK, map[string]any{"activation": activation, "revocation": revocation, "now": time.Now().UnixMilli()})
}

func (s *Server) revocationAck(w http.ResponseWriter, r *http.Request) {
	device, err := s.authenticateDevice(r)
	if s.writeDeviceAuthFailure(w, device, err) {
		return
	}
	var input struct {
		RequestID   string `json:"requestId"`
		WorkspaceID string `json:"workspaceId"`
	}
	if webutil.DecodeJSON(r, 64<<10, &input) != nil || input.RequestID == "" {
		webutil.JSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	acked, err := s.Activation.AcknowledgeRevocation(r.Context(), device.UserID, device.DeviceID, input.WorkspaceID, input.RequestID)
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": "revocation_ack_failed"})
		return
	}
	webutil.JSON(w, http.StatusOK, map[string]any{"ok": true, "acked": acked})
}

func (s *Server) ListenAndServe() error {
	slog.Info("CodeLocal Go Cloud starting", "addr", s.HTTP.Addr, "version", version.Version, "protocolVersion", protocol.Version, "gateway", s.InstanceID)
	err := s.HTTP.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	err := s.HTTP.Shutdown(ctx)
	s.Hub.Close()
	_ = s.Coordinator.Close()
	_ = s.Activation.Close()
	s.Store.Close()
	return err
}
