package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0xmarkhydra/codelocal/internal/mcpconfig"
	"github.com/jackc/pgx/v5"
)

const mcpConnectionMigrationSQL = `
CREATE TABLE codelocal_mcp_connections (
  user_id TEXT NOT NULL REFERENCES codelocal_users(id) ON DELETE CASCADE,
  target TEXT NOT NULL CHECK(target IN ('online','local')),
  device_id TEXT NOT NULL DEFAULT '',
  workspace_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  config JSONB NOT NULL,
  credential_nonce BYTEA NOT NULL,
  credential_ciphertext BYTEA NOT NULL,
  state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','configured','ready','error')),
  tool_count INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  connected_at BIGINT NOT NULL DEFAULT 0,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY(user_id,target,device_id,workspace_id,name),
  CHECK(
    (target='online' AND device_id='' AND workspace_id='') OR
    (target='local' AND device_id<>'' AND workspace_id='')
  )
);
CREATE INDEX idx_codelocal_mcp_connections_owner
  ON codelocal_mcp_connections(user_id,target,updated_at DESC);
`

type MCPConnection struct {
	UserID         string           `json:"-"`
	Target         string           `json:"target"`
	DeviceID       string           `json:"deviceId,omitempty"`
	WorkspaceID    string           `json:"workspaceId,omitempty"`
	Server         mcpconfig.Server `json:"server"`
	State          string           `json:"state"`
	ToolCount      int              `json:"toolCount"`
	LastError      string           `json:"lastError,omitempty"`
	ConnectedAt    int64            `json:"connectedAt,omitempty"`
	UpdatedAt      int64            `json:"updatedAt"`
	ConfigEditable bool             `json:"configEditable"`
}

func mcpConnectionBinding(user, target, device, workspace, name string) string {
	return fmt.Sprintf("mcp-connection-v1:%d:%s:%d:%s:%d:%s:%d:%s:%d:%s", len(user), user, len(target), target, len(device), device, len(workspace), workspace, len(name), name)
}

func normalizeMCPConnection(c MCPConnection) (MCPConnection, error) {
	c.UserID = strings.TrimSpace(c.UserID)
	c.Target = strings.ToLower(strings.TrimSpace(c.Target))
	c.DeviceID = strings.TrimSpace(c.DeviceID)
	c.WorkspaceID = strings.TrimSpace(c.WorkspaceID)
	c.Server.Name = strings.TrimSpace(c.Server.Name)
	if c.UserID == "" || c.Server.Name == "" {
		return c, errors.New("invalid MCP connection identity")
	}
	switch c.Target {
	case "online":
		c.DeviceID, c.WorkspaceID = "", ""
	case "local":
		if c.DeviceID == "" {
			return c, errors.New("local MCP requires a device")
		}
		c.WorkspaceID = ""
	default:
		return c, errors.New("invalid MCP execution target")
	}
	if c.State == "" {
		c.State = "pending"
	}
	switch c.State {
	case "pending", "configured", "ready", "error":
	default:
		return c, errors.New("invalid MCP connection state")
	}
	if c.ToolCount < 0 {
		c.ToolCount = 0
	}
	if c.UpdatedAt <= 0 {
		c.UpdatedAt = time.Now().UnixMilli()
	}
	if c.State == "ready" && c.ConnectedAt <= 0 {
		c.ConnectedAt = c.UpdatedAt
	}
	return c, nil
}

func (s *Store) SaveMCPConnection(ctx context.Context, c MCPConnection, secrets map[string]string) (MCPConnection, error) {
	if s == nil || s.DB == nil {
		return c, errors.New("MCP connection store unavailable")
	}
	var err error
	c, err = normalizeMCPConnection(c)
	if err != nil {
		return c, err
	}
	config, err := json.Marshal(c.Server)
	if err != nil {
		return c, err
	}
	binding := mcpConnectionBinding(c.UserID, c.Target, c.DeviceID, c.WorkspaceID, c.Server.Name)
	nonce, ciphertext, err := sealPluginValue(secrets, binding)
	if err != nil {
		return c, err
	}
	err = s.DB.QueryRow(ctx, `INSERT INTO codelocal_mcp_connections(
 user_id,target,device_id,workspace_id,name,config,credential_nonce,credential_ciphertext,state,tool_count,last_error,connected_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT(user_id,target,device_id,workspace_id,name) DO UPDATE SET
 config=EXCLUDED.config, credential_nonce=EXCLUDED.credential_nonce, credential_ciphertext=EXCLUDED.credential_ciphertext,
 state=EXCLUDED.state, tool_count=EXCLUDED.tool_count, last_error=EXCLUDED.last_error,
 connected_at=EXCLUDED.connected_at, updated_at=GREATEST(EXCLUDED.updated_at,codelocal_mcp_connections.updated_at+1)
RETURNING updated_at`, c.UserID, c.Target, c.DeviceID, c.WorkspaceID, c.Server.Name, config, nonce, ciphertext,
		c.State, c.ToolCount, c.LastError, c.ConnectedAt, c.UpdatedAt).Scan(&c.UpdatedAt)
	return c, err
}

func scanMCPConnection(row pgx.Row) (MCPConnection, []byte, []byte, error) {
	var c MCPConnection
	var config, nonce, ciphertext []byte
	err := row.Scan(&c.UserID, &c.Target, &c.DeviceID, &c.WorkspaceID, &c.Server.Name, &config, &nonce, &ciphertext,
		&c.State, &c.ToolCount, &c.LastError, &c.ConnectedAt, &c.UpdatedAt)
	if err != nil {
		return c, nil, nil, err
	}
	if err := json.Unmarshal(config, &c.Server); err != nil {
		return c, nil, nil, err
	}
	return c, nonce, ciphertext, nil
}

const mcpConnectionSelect = `SELECT user_id,target,device_id,workspace_id,name,config,credential_nonce,credential_ciphertext,state,tool_count,last_error,connected_at,updated_at FROM codelocal_mcp_connections`

func (s *Store) MCPConnection(ctx context.Context, user, target, device, workspace, name string) (MCPConnection, map[string]string, bool, error) {
	if s == nil || s.DB == nil {
		return MCPConnection{}, nil, false, errors.New("MCP connection store unavailable")
	}
	c, nonce, ciphertext, err := scanMCPConnection(s.DB.QueryRow(ctx, mcpConnectionSelect+` WHERE user_id=$1 AND target=$2 AND device_id=$3 AND workspace_id=$4 AND name=$5`, user, target, device, workspace, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPConnection{}, nil, false, nil
	}
	if err != nil {
		return c, nil, false, err
	}
	secrets := map[string]string{}
	binding := mcpConnectionBinding(c.UserID, c.Target, c.DeviceID, c.WorkspaceID, c.Server.Name)
	if err := openPluginValue(nonce, ciphertext, binding, &secrets); err != nil {
		return c, nil, false, err
	}
	return c, secrets, true, nil
}

func (s *Store) ListMCPConnections(ctx context.Context, user string) ([]MCPConnection, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("MCP connection store unavailable")
	}
	rows, err := s.DB.Query(ctx, mcpConnectionSelect+` WHERE user_id=$1 ORDER BY target,device_id,workspace_id,name`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPConnection{}
	for rows.Next() {
		var c MCPConnection
		var config, nonce, ciphertext []byte
		if err := rows.Scan(&c.UserID, &c.Target, &c.DeviceID, &c.WorkspaceID, &c.Server.Name, &config, &nonce, &ciphertext,
			&c.State, &c.ToolCount, &c.LastError, &c.ConnectedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(config, &c.Server); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) MaterializeLocalMCPConnections(ctx context.Context, user, device string) ([]mcpconfig.MaterializedServer, error) {
	rows, err := s.DB.Query(ctx, mcpConnectionSelect+` WHERE user_id=$1 AND target='local' AND device_id=$2 AND workspace_id='' ORDER BY name`, user, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mcpconfig.MaterializedServer{}
	for rows.Next() {
		c, nonce, ciphertext, err := scanMCPConnection(rows)
		if err != nil {
			return nil, err
		}
		secrets := map[string]string{}
		binding := mcpConnectionBinding(c.UserID, c.Target, c.DeviceID, c.WorkspaceID, c.Server.Name)
		if err := openPluginValue(nonce, ciphertext, binding, &secrets); err != nil {
			return nil, err
		}
		out = append(out, mcpconfig.MaterializedServer{Server: c.Server, Secrets: secrets})
	}
	return out, rows.Err()
}

func (s *Store) UpdateLocalMCPActual(ctx context.Context, user, device string, actual mcpconfig.Actual) error {
	state := strings.TrimSpace(actual.State)
	if state != "configured" && state != "ready" && state != "error" {
		return errors.New("invalid MCP actual state")
	}
	connectedAt := int64(0)
	if state == "ready" {
		connectedAt = time.Now().UnixMilli()
	}
	_, err := s.DB.Exec(ctx, `UPDATE codelocal_mcp_connections SET state=$4,tool_count=$5,last_error=$6,
 connected_at=CASE WHEN $4='ready' THEN GREATEST(connected_at,$7) ELSE connected_at END,updated_at=GREATEST(updated_at+1,$7)
WHERE user_id=$1 AND target='local' AND device_id=$2 AND workspace_id='' AND name=$3`,
		user, device, actual.Name, state, actual.ToolCount, actual.LastError, connectedAt)
	return err
}

func (s *Store) DeleteMCPConnection(ctx context.Context, user, target, device, workspace, name string) (bool, error) {
	if s == nil || s.DB == nil {
		return false, errors.New("MCP connection store unavailable")
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM codelocal_mcp_connections WHERE user_id=$1 AND target=$2 AND device_id=$3 AND workspace_id=$4 AND name=$5`, user, target, device, workspace, name)
	return err == nil && tag.RowsAffected() > 0, err
}
