package cloudserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/cloud"
	"github.com/0xmarkhydra/codelocal/internal/cloudmcp"
	"github.com/0xmarkhydra/codelocal/internal/gateway"
	"github.com/0xmarkhydra/codelocal/internal/mcpconfig"
	"github.com/0xmarkhydra/codelocal/internal/webutil"
)

type mcpConnectionsInput struct {
	Target   string            `json:"target"`
	DeviceID string            `json:"deviceId,omitempty"`
	Config   string            `json:"config"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}

type runtimeMCPServer struct {
	Name        string            `json:"name"`
	Enabled     bool              `json:"enabled"`
	Managed     bool              `json:"managed"`
	Transport   string            `json:"transport"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	CWD         string            `json:"cwd"`
	Env         map[string]string `json:"env"`
	URL         string            `json:"url"`
	Headers     map[string]any    `json:"headers"`
	Connected   bool              `json:"connected"`
	ToolsCached int               `json:"toolsCached"`
}

func runtimeMCPConnections(value any, userID, deviceID string) []cloud.MCPConnection {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var payload struct {
		Servers []runtimeMCPServer `json:"servers"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	now := time.Now().UnixMilli()
	out := make([]cloud.MCPConnection, 0, len(payload.Servers))
	for _, item := range payload.Servers {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		if item.Managed && name == "penpot" {
			continue
		}
		state := "configured"
		lastError := ""
		if !item.Enabled {
			state = "error"
			lastError = "MCP server is disabled."
		} else if item.Connected && item.ToolsCached > 0 {
			state = "ready"
		}
		env := make(map[string]mcpconfig.Reference, len(item.Env))
		for target, source := range item.Env {
			if strings.TrimSpace(target) == "" || strings.TrimSpace(source) == "" {
				continue
			}
			env[target] = mcpconfig.Reference{Source: source}
		}
		connection := cloud.MCPConnection{
			UserID:   userID,
			Target:   "local",
			DeviceID: deviceID,
			Server: mcpconfig.Server{
				Name: name, Enabled: item.Enabled, Transport: item.Transport,
				Command: item.Command, Args: append([]string(nil), item.Args...), CWD: item.CWD, Env: env, URL: item.URL,
			},
			State: state, ToolCount: item.ToolsCached, LastError: lastError, UpdatedAt: now,
			ConfigEditable: !item.Managed && len(item.Headers) == 0,
		}
		if state == "ready" {
			connection.ConnectedAt = now
		}
		out = append(out, connection)
	}
	return out
}

func mcpConnectionKey(connection cloud.MCPConnection) string {
	return connection.Target + "\x00" + connection.DeviceID + "\x00" + connection.Server.Name
}

func (s *Server) mcpConnectionsListAPI(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticatedAPIIdentity(w, r)
	if !ok {
		return
	}
	connections, err := s.Store.ListMCPConnections(r.Context(), identity.User.ID)
	if err != nil {
		webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp_connections_unavailable"})
		return
	}

	merged := make(map[string]cloud.MCPConnection, len(connections))
	order := make([]string, 0, len(connections))
	for _, connection := range connections {
		connection.ConfigEditable = true
		key := mcpConnectionKey(connection)
		if _, exists := merged[key]; !exists {
			order = append(order, key)
		}
		merged[key] = connection
	}

	if s.Workspaces != nil && s.Hub != nil {
		if catalog, catalogErr := s.Workspaces.Catalog(r.Context(), identity.User.ID); catalogErr == nil {
			seenDevices := map[string]bool{}
			probed := 0
			for _, workspace := range catalog {
				if probed >= 4 || !workspace.RuntimeOnline || seenDevices[workspace.DeviceID] || !pluginCapability(&workspace, "mcpHub") {
					continue
				}
				seenDevices[workspace.DeviceID] = true
				probed++
				ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
				result, callErr := s.Hub.Call(ctx, identity.User.ID, workspace.Key, "mcp-discovery", "mcp_list", map[string]any{}, false, cloud.RandomHex(16))
				cancel()
				if callErr != nil || !result.OK {
					continue
				}
				for _, discovered := range runtimeMCPConnections(result.Result, identity.User.ID, workspace.DeviceID) {
					key := mcpConnectionKey(discovered)
					if existing, exists := merged[key]; exists {
						discovered.ConnectedAt = existing.ConnectedAt
						discovered.ConfigEditable = true
						if len(existing.Server.Env) > 0 {
							discovered.Server.Env = existing.Server.Env
						}
						if len(existing.Server.Headers) > 0 {
							discovered.Server.Headers = existing.Server.Headers
						}
						if discovered.State == "ready" && discovered.ConnectedAt == 0 {
							discovered.ConnectedAt = discovered.UpdatedAt
						}
					} else {
						order = append(order, key)
					}
					merged[key] = discovered
				}
			}
		}
	}

	items := make([]cloud.MCPConnection, 0, len(order))
	for _, key := range order {
		items = append(items, merged[key])
	}
	webutil.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func validateMCPSecrets(servers []mcpconfig.Server, secrets map[string]string) error {
	needed := map[string]bool{}
	for _, server := range servers {
		for _, ref := range server.Env {
			needed[ref.Source] = true
		}
		for _, ref := range server.Headers {
			needed[ref.Source] = true
		}
	}
	for name, value := range secrets {
		if !needed[name] {
			return errors.New("secret value does not match an MCP config reference")
		}
		if len(value) > 16384 || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("invalid MCP secret value")
		}
	}
	return nil
}

func mcpSecretsForServer(server mcpconfig.Server, provided, existing map[string]string) map[string]string {
	out := map[string]string{}
	appendRef := func(ref mcpconfig.Reference) {
		if value, ok := provided[ref.Source]; ok {
			out[ref.Source] = value
			return
		}
		if value, ok := existing[ref.Source]; ok {
			out[ref.Source] = value
		}
	}
	for _, ref := range server.Env {
		appendRef(ref)
	}
	for _, ref := range server.Headers {
		appendRef(ref)
	}
	return out
}

func onlineMCPConfig(server mcpconfig.Server, secrets map[string]string) (cloudmcp.Config, error) {
	endpoint, err := cloudmcp.ValidateEndpoint(server.URL)
	if err != nil {
		return cloudmcp.Config{}, err
	}
	cfg := cloudmcp.Config{Endpoint: endpoint}
	for header, ref := range server.Headers {
		if !strings.EqualFold(header, "Authorization") || ref.Prefix != "Bearer " {
			return cfg, errors.New("online MCP currently supports only Bearer Authorization headers")
		}
		secret := secrets[ref.Source]
		if strings.TrimSpace(secret) == "" {
			return cfg, errors.New("online MCP authorization secret is required")
		}
		cfg.Bearer = secret
	}
	return cfg, nil
}

func (s *Server) accountOwnsDevice(ctx context.Context, userID, deviceID string) bool {
	devices, err := s.Store.ListDevices(ctx, userID)
	if err != nil {
		return false
	}
	for _, device := range devices {
		if device.DeviceID == deviceID && device.RevokedAt == 0 {
			return true
		}
	}
	return false
}

func (s *Server) activeWorkspaceForDevice(ctx context.Context, userID, deviceID string) (*gateway.WorkspaceView, error) {
	if s.Workspaces == nil {
		return nil, errors.New("workspace service unavailable")
	}
	catalog, err := s.Workspaces.Catalog(ctx, userID)
	if err != nil {
		return nil, err
	}
	for index := range catalog {
		if catalog[index].DeviceID != deviceID {
			continue
		}
		workspace, err := s.pluginWorkspaceByKey(ctx, userID, catalog[index].Key)
		if err == nil {
			return workspace, nil
		}
	}
	return nil, errors.New("device is offline")
}

func (s *Server) tryApplyLocalMCP(ctx context.Context, userID, deviceID string, server mcpconfig.Server, secrets map[string]string) (cloud.MCPConnection, bool) {
	workspace, err := s.activeWorkspaceForDevice(ctx, userID, deviceID)
	if err != nil || s.Hub == nil {
		return cloud.MCPConnection{}, false
	}
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	result, err := s.Hub.Call(callCtx, userID, workspace.Key, "mcp-config", "mcp_configure", map[string]any{
		"server":  server,
		"secrets": secrets,
	}, true, cloud.RandomHex(16))
	if err != nil || !result.OK {
		return cloud.MCPConnection{}, false
	}
	configured := pluginConfigureResultFrom(result.Result)
	state := "configured"
	if configured.Connected {
		state = "ready"
	} else if configured.Error != "" {
		state = "error"
	}
	connection := cloud.MCPConnection{
		UserID: userID, Target: "local", DeviceID: deviceID, Server: server,
		State: state, ToolCount: configured.ToolCount, LastError: configured.Error,
	}
	return connection, true
}

func (s *Server) mcpConnectionsCreateAPI(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.pluginMutationIdentity(w, r, true)
	if !ok {
		return
	}
	var input mcpConnectionsInput
	if webutil.DecodeJSON(r, 256<<10, &input) != nil {
		webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mcp_connection"})
		return
	}
	input.Target = strings.ToLower(strings.TrimSpace(input.Target))
	mode := mcpconfig.ModeLocal
	if input.Target == "online" {
		mode = mcpconfig.ModeOnline
	} else if input.Target != "local" {
		webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mcp_target"})
		return
	}
	servers, err := mcpconfig.Parse(input.Config, mode)
	if err != nil || len(servers) > 32 {
		detail := "Invalid MCP configuration."
		if err != nil {
			detail = err.Error()
		}
		webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mcp_config", "detail": detail})
		return
	}
	if input.Secrets == nil {
		input.Secrets = map[string]string{}
	}
	if err := validateMCPSecrets(servers, input.Secrets); err != nil {
		webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mcp_secrets", "detail": err.Error()})
		return
	}
	if input.Target == "local" {
		input.DeviceID = strings.TrimSpace(input.DeviceID)
		if input.DeviceID == "" || !s.accountOwnsDevice(r.Context(), identity.User.ID, input.DeviceID) {
			webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mcp_device"})
			return
		}
	}

	created := make([]cloud.MCPConnection, 0, len(servers))
	for _, server := range servers {
		existingSecrets := map[string]string{}
		if _, secrets, found, lookupErr := s.Store.MCPConnection(r.Context(), identity.User.ID, input.Target, input.DeviceID, "", server.Name); lookupErr != nil {
			webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp_connection_store_failed"})
			return
		} else if found {
			existingSecrets = secrets
		}
		serverSecrets := mcpSecretsForServer(server, input.Secrets, existingSecrets)
		connection := cloud.MCPConnection{UserID: identity.User.ID, Target: input.Target, DeviceID: input.DeviceID, Server: server, State: "pending", ConfigEditable: true}
		if input.Target == "local" {
			stored, storeErr := s.Store.SaveMCPConnection(r.Context(), connection, serverSecrets)
			if storeErr != nil {
				webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp_connection_store_failed"})
				return
			}
			connection = stored
			connection.ConfigEditable = true
			if applied, appliedOK := s.tryApplyLocalMCP(r.Context(), identity.User.ID, input.DeviceID, server, serverSecrets); appliedOK {
				connection.State, connection.ToolCount, connection.LastError = applied.State, applied.ToolCount, applied.LastError
				connection, storeErr = s.Store.SaveMCPConnection(r.Context(), connection, serverSecrets)
				if storeErr != nil {
					webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp_connection_store_failed"})
					return
				}
				connection.ConfigEditable = true
			}
			created = append(created, connection)
			continue
		}

		cfg, cfgErr := onlineMCPConfig(server, serverSecrets)
		if cfgErr != nil {
			connection.State, connection.LastError = "error", cfgErr.Error()
		} else if s.CloudMCP == nil {
			connection.State, connection.LastError = "error", "Cloud MCP runtime is unavailable."
		} else {
			tools, discoverErr := s.CloudMCP.Discover(r.Context(), identity.User.ID, cfg)
			if discoverErr != nil {
				connection.State, connection.LastError = "error", discoverErr.Error()
			} else {
				connection.State, connection.ToolCount = "ready", len(tools)
			}
		}
		stored, storeErr := s.Store.SaveMCPConnection(r.Context(), connection, serverSecrets)
		if storeErr != nil {
			webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp_connection_store_failed"})
			return
		}
		stored.ConfigEditable = true
		created = append(created, stored)
	}
	webutil.JSON(w, http.StatusOK, map[string]any{"ok": true, "items": created})
}

func (s *Server) mcpConnectionDeleteAPI(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.pluginMutationIdentity(w, r, true)
	if !ok {
		return
	}
	target := strings.ToLower(strings.TrimSpace(r.PathValue("target")))
	name := strings.TrimSpace(r.PathValue("name"))
	deviceID := ""
	if target == "local" {
		deviceID = strings.TrimSpace(r.URL.Query().Get("deviceId"))
		if !s.accountOwnsDevice(r.Context(), identity.User.ID, deviceID) {
			webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mcp_device"})
			return
		}
	} else if target != "online" {
		webutil.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_mcp_target"})
		return
	}
	removed, err := s.Store.DeleteMCPConnection(r.Context(), identity.User.ID, target, deviceID, "", name)
	if err != nil {
		webutil.JSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp_connection_delete_failed"})
		return
	}
	runtimeRemoved := false
	if target == "local" {
		if workspace, workspaceErr := s.activeWorkspaceForDevice(r.Context(), identity.User.ID, deviceID); workspaceErr == nil && s.Hub != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			result, callErr := s.Hub.Call(ctx, identity.User.ID, workspace.Key, "mcp-config", "mcp_remove", map[string]any{"name": name}, true, cloud.RandomHex(16))
			cancel()
			runtimeRemoved = callErr == nil && result.OK
		}
	}
	webutil.JSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed || runtimeRemoved})
}
