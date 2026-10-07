package cloudserver

import (
	"net/http"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/webutil"
)

func (s *Server) registerPluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/plugins", s.pluginsResourceAPI)
	mux.HandleFunc("GET /api/v1/mcp/connections", s.mcpConnectionsListAPI)
	mux.Handle("POST /api/v1/mcp/connections", webutil.RateLimit(s.Store, webutil.RateLimitOptions{Scope: "mcp-connect-ip", Limit: 30, Window: 10 * time.Minute}, dashboardJSONCSRF(http.HandlerFunc(s.mcpConnectionsCreateAPI))))
	mux.Handle("PUT /api/v1/mcp/connections/{target}/{name}", webutil.RateLimit(s.Store, webutil.RateLimitOptions{Scope: "mcp-update-ip", Limit: 60, Window: 10 * time.Minute}, dashboardJSONCSRF(http.HandlerFunc(s.mcpConnectionUpdateAPI))))
	mux.Handle("DELETE /api/v1/mcp/connections/{target}/{name}", webutil.RateLimit(s.Store, webutil.RateLimitOptions{Scope: "mcp-disconnect-ip", Limit: 60, Window: 10 * time.Minute}, dashboardJSONCSRF(http.HandlerFunc(s.mcpConnectionDeleteAPI))))
	mux.HandleFunc("GET /api/v1/plugin-approvals/{approvalID}", s.pluginApprovalDetailsAPI)
	mux.HandleFunc("GET /api/v1/plugins/oauth/client", s.pluginOAuthMetadata)
	mux.HandleFunc("GET /api/v1/plugins/oauth/callback", s.pluginOAuthCallback)
	mux.Handle("POST /api/v1/plugins/{pluginID}/oauth", webutil.RateLimit(s.Store, webutil.RateLimitOptions{Scope: "plugin-oauth-ip", Limit: 10, Window: time.Minute}, dashboardJSONCSRF(http.HandlerFunc(s.pluginOAuthStart))))
	mux.Handle("POST /api/v1/plugin-approvals/{approvalID}", webutil.RateLimit(s.Store, webutil.RateLimitOptions{
		Scope: "plugin-approve-ip", Limit: 30, Window: time.Minute,
	}, dashboardJSONCSRF(http.HandlerFunc(s.pluginApprovalAPI))))
	install := dashboardJSONCSRF(http.HandlerFunc(s.pluginInstallAPI))
	uninstall := dashboardJSONCSRF(http.HandlerFunc(s.pluginUninstallAPI))
	connect := dashboardJSONCSRF(http.HandlerFunc(s.pluginConnectAPI))
	disconnect := dashboardJSONCSRF(http.HandlerFunc(s.pluginDisconnectAPI))
	mux.Handle("POST /api/v1/plugins/{pluginID}/install", webutil.RateLimit(s.Store, webutil.RateLimitOptions{
		Scope: "plugin-install-ip", Limit: 30, Window: 10 * time.Minute,
	}, install))
	mux.Handle("DELETE /api/v1/plugins/{pluginID}/install", webutil.RateLimit(s.Store, webutil.RateLimitOptions{
		Scope: "plugin-uninstall-ip", Limit: 60, Window: 10 * time.Minute,
	}, uninstall))
	mux.Handle("POST /api/v1/plugins/{pluginID}/connections", webutil.RateLimit(s.Store, webutil.RateLimitOptions{
		Scope: "plugin-connect-ip", Limit: 30, Window: 10 * time.Minute,
	}, connect))
	mux.Handle("DELETE /api/v1/plugins/{pluginID}/connections/{deviceID}", webutil.RateLimit(s.Store, webutil.RateLimitOptions{
		Scope: "plugin-disconnect-ip", Limit: 60, Window: 10 * time.Minute,
	}, disconnect))
}
