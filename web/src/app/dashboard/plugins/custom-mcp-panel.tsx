"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslations } from "@/lib/i18n/provider";
import { AppIcon } from "../app-icon";
import styles from "./custom-mcp-panel.module.css";

type Device = {
  deviceId: string;
  deviceName: string;
  status: "online" | "offline" | "revoked";
};

type MCPReference = { source: string; prefix?: string };

type MCPServer = {
  name: string;
  enabled: boolean;
  transport: "stdio" | "http";
  command?: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, MCPReference>;
  url?: string;
  headers?: Record<string, MCPReference>;
};

type MCPConnection = {
  target: "online" | "local";
  deviceId?: string;
  server: MCPServer;
  state: "pending" | "configured" | "ready" | "error";
  toolCount: number;
  lastError?: string;
  connectedAt?: number;
  updatedAt: number;
  configEditable: boolean;
};

type Account = { csrf: string };
type Notice = { kind: "success" | "error"; text: string } | null;
type SetupMode = "quick" | "json";

function secretReferences(raw: string) {
  const found = new Set<string>();
  for (const match of raw.matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g)) found.add(match[1]);
  return [...found];
}

function serverSummary(server: MCPServer, remoteLabel: string) {
  if (server.transport === "stdio") return [server.command, ...(server.args ?? [])].filter(Boolean).join(" ");
  return server.url || remoteLabel;
}

function statusLabel(state: MCPConnection["state"]) {
  if (state === "ready") return "Ready";
  if (state === "error") return "Failed";
  if (state === "configured") return "Configured";
  return "Pending";
}

function serverConfig(item: MCPConnection) {
  const server: Record<string, unknown> = {};
  if (item.server.transport === "stdio") {
    server.command = item.server.command || "";
    if ((item.server.args ?? []).length > 0) server.args = item.server.args;
    if (item.server.cwd) server.cwd = item.server.cwd;
  } else {
    server.url = item.server.url || "";
  }
  if (!item.server.enabled) server.enabled = false;
  if (item.server.env && Object.keys(item.server.env).length > 0) {
    server.env = Object.fromEntries(Object.entries(item.server.env).map(([key, ref]) => [key, `\${${ref.source}}`]));
  }
  if (item.server.headers && Object.keys(item.server.headers).length > 0) {
    server.headers = Object.fromEntries(Object.entries(item.server.headers).map(([key, ref]) => [key, `${ref.prefix ?? ""}\${${ref.source}}`]));
  }
  return server;
}

function serverJSON(item: MCPConnection) {
  return JSON.stringify({ mcpServers: { [item.server.name]: serverConfig(item) } }, null, 2);
}

function deviceLabel(devices: Device[], unknownLabel: string, id?: string) {
  return devices.find((device) => device.deviceId === id)?.deviceName || id || unknownLabel;
}

function normalizedName(value: string) {
  return value.trim().replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 64);
}

export function CustomMCPPanel({ onCountChange }: { onCountChange?: (count: number) => void }) {
  const { t } = useTranslations();
  const [connections, setConnections] = useState<MCPConnection[]>([]);
  const [devices, setDevices] = useState<Device[]>([]);
  const [account, setAccount] = useState<Account | null>(null);
  const [loading, setLoading] = useState(true);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [target, setTarget] = useState<"online" | "local">("online");
  const [setupMode, setSetupMode] = useState<SetupMode>("quick");
  const [deviceId, setDeviceId] = useState("");
  const [name, setName] = useState("");
  const [url, setURL] = useState("");
  const [command, setCommand] = useState("");
  const [argsText, setArgsText] = useState("");
  const [bearerToken, setBearerToken] = useState("");
  const [config, setConfig] = useState("");
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [editing, setEditing] = useState<MCPConnection | null>(null);
  const [notice, setNotice] = useState<Notice>(null);

  const refresh = useCallback(async () => {
    const [connectionsResponse, devicesResponse, accountResponse] = await Promise.all([
      fetch("/api/v1/mcp/connections", { credentials: "same-origin", cache: "no-store" }),
      fetch("/api/v1/devices", { credentials: "same-origin", cache: "no-store" }),
      fetch("/api/v1/account", { credentials: "same-origin", cache: "no-store" }),
    ]);
    if (!connectionsResponse.ok || !devicesResponse.ok || !accountResponse.ok) {
      throw new Error(t("MCP connections are temporarily unavailable."));
    }
    const connectionPayload = await connectionsResponse.json() as { items?: MCPConnection[] };
    const devicePayload = await devicesResponse.json() as { items?: Device[] };
    const accountPayload = await accountResponse.json() as Account;
    const nextDevices = Array.isArray(devicePayload.items) ? devicePayload.items.filter((item) => item.status !== "revoked") : [];
    const nextConnections = Array.isArray(connectionPayload.items) ? connectionPayload.items : [];
    setConnections(nextConnections);
    onCountChange?.(nextConnections.length);
    setDevices(nextDevices);
    setAccount(accountPayload);
    setDeviceId((current) => current || nextDevices.find((item) => item.status === "online")?.deviceId || nextDevices[0]?.deviceId || "");
    return nextConnections;
  }, [onCountChange, t]);

  useEffect(() => {
    let cancelled = false;
    const timer = window.setTimeout(() => {
      void refresh()
        .catch((error) => {
          if (!cancelled) setNotice({ kind: "error", text: error instanceof Error ? error.message : t("MCP connections are temporarily unavailable.") });
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    }, 0);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [refresh, t]);

  const refs = useMemo(() => secretReferences(config), [config]);
  const localConfigGroups = useMemo(() => {
    const groups = new Map<string, MCPConnection[]>();
    for (const item of connections) {
      if (item.target !== "local") continue;
      const key = item.deviceId || "local";
      const list = groups.get(key) ?? [];
      list.push(item);
      groups.set(key, list);
    }
    return [...groups.entries()].map(([id, items]) => ({
      deviceId: id,
      json: JSON.stringify({
        mcpServers: Object.fromEntries(items.map((item) => [item.server.name, serverConfig(item)])),
      }, null, 2),
      count: items.length,
    }));
  }, [connections]);

  function resetForm(nextTarget: "online" | "local" = target) {
    setTarget(nextTarget);
    setSetupMode("quick");
    setName("");
    setURL("");
    setCommand("");
    setArgsText("");
    setBearerToken("");
    setConfig("");
    setSecrets({});
  }

  function openAddDialog() {
    setEditing(null);
    resetForm("online");
    setNotice(null);
    setDialogOpen(true);
  }

  function openEditDialog(item: MCPConnection) {
    setEditing(item);
    setTarget(item.target);
    setSetupMode("json");
    setDeviceId(item.deviceId || "");
    setName(item.server.name);
    setURL(item.server.url || "");
    setCommand(item.server.command || "");
    setArgsText((item.server.args ?? []).join("\n"));
    setBearerToken("");
    setConfig(serverJSON(item));
    setSecrets({});
    setNotice(null);
    setDialogOpen(true);
  }

  async function scanMCPs() {
    setScanning(true);
    setNotice(null);
    try {
      const items = await refresh();
      const localCount = items.filter((item) => item.target === "local").length;
      setNotice({ kind: "success", text: t("Scan complete. Found {count} local MCPs.", { count: localCount }) });
    } catch (error) {
      setNotice({ kind: "error", text: error instanceof Error ? error.message : t("MCP connections are temporarily unavailable.") });
    } finally {
      setScanning(false);
    }
  }

  function selectTarget(next: "online" | "local") {
    resetForm(next);
  }

  async function copyConfig(raw: string) {
    try {
      await navigator.clipboard.writeText(raw);
      setNotice({ kind: "success", text: t("MCP JSON copied.") });
    } catch {
      setNotice({ kind: "error", text: t("Could not copy MCP JSON.") });
    }
  }

  function buildQuickConfig() {
    const serverName = normalizedName(name);
    if (!serverName) throw new Error(t("Enter a name for this MCP."));
    if (target === "online") {
      const endpoint = url.trim();
      if (!endpoint) throw new Error(t("Enter the MCP URL."));
      const server: Record<string, unknown> = { url: endpoint };
      const quickSecrets: Record<string, string> = {};
      if (bearerToken.trim()) {
        server.headers = { Authorization: "Bearer ${MCP_TOKEN}" };
        quickSecrets.MCP_TOKEN = bearerToken.trim();
      }
      return { config: JSON.stringify({ mcpServers: { [serverName]: server } }, null, 2), secrets: quickSecrets };
    }
    const executable = command.trim();
    if (!executable) throw new Error(t("Enter the command used to start this MCP."));
    const args = argsText.split("\n").map((value) => value.trim()).filter(Boolean);
    return {
      config: JSON.stringify({ mcpServers: { [serverName]: { command: executable, ...(args.length > 0 ? { args } : {}) } } }, null, 2),
      secrets: {},
    };
  }

  async function addConnection() {
    if (!account?.csrf || (target === "local" && !deviceId)) return;
    setSaving(true);
    setNotice(null);
    try {
      const prepared = setupMode === "quick" ? buildQuickConfig() : { config: config.trim(), secrets };
      if (!prepared.config) throw new Error(t("Paste an MCP configuration first."));
      if (editing) {
        let parsed: { mcpServers?: Record<string, unknown> };
        try {
          parsed = JSON.parse(prepared.config) as { mcpServers?: Record<string, unknown> };
        } catch {
          throw new Error(t("MCP JSON is invalid."));
        }
        const names = Object.keys(parsed.mcpServers ?? {});
        if (names.length !== 1 || names[0] !== editing.server.name) {
          throw new Error(t("Keep the MCP name unchanged while editing."));
        }
      }
      const response = await fetch("/api/v1/mcp/connections", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": account.csrf },
        body: JSON.stringify({
          target,
          deviceId: target === "local" ? deviceId : undefined,
          config: prepared.config,
          secrets: prepared.secrets,
        }),
      });
      const payload = await response.json().catch(() => ({})) as { detail?: string; error?: string; items?: MCPConnection[] };
      if (!response.ok) throw new Error(payload.detail || payload.error || t("Could not add this MCP."));
      await refresh();
      setDialogOpen(false);
      const hasError = payload.items?.some((item) => item.state === "error");
      const hasPending = payload.items?.some((item) => item.state === "pending" || item.state === "configured");
      setNotice({
        kind: hasError ? "error" : "success",
        text: hasError
          ? t("MCP saved, but its connection needs attention.")
          : hasPending
            ? t("MCP saved. It will finish setup when the selected device is available.")
            : editing ? t("MCP updated and ready.") : t("MCP added and ready."),
      });
    } catch (error) {
      setNotice({ kind: "error", text: error instanceof Error ? error.message : t("Could not add this MCP.") });
    } finally {
      setSaving(false);
      setBearerToken("");
    }
  }

  async function removeConnection(item: MCPConnection) {
    if (!account?.csrf || !window.confirm(t("Remove {name}?", { name: item.server.name }))) return;
    const query = item.target === "local" && item.deviceId ? `?deviceId=${encodeURIComponent(item.deviceId)}` : "";
    const response = await fetch(`/api/v1/mcp/connections/${encodeURIComponent(item.target)}/${encodeURIComponent(item.server.name)}${query}`, {
      method: "DELETE",
      credentials: "same-origin",
      headers: { "X-CSRF-Token": account.csrf },
    });
    if (!response.ok) {
      setNotice({ kind: "error", text: t("Could not remove this MCP.") });
      return;
    }
    await refresh();
    setNotice({ kind: "success", text: t("MCP removed.") });
  }

  return (
    <section className={styles.panel}>
      <div className={styles.panelHeader}>
        <div>
          <h2>{t("Custom MCP")}</h2>
          <p>{t("Local MCP JSON is the source of truth. Scan, edit, add, or remove servers from this dashboard.")}</p>
        </div>
        <div className={styles.headerActions}>
          <button className={styles.secondaryButton} type="button" disabled={scanning} onClick={() => void scanMCPs()}>
            <AppIcon name="refresh" size={15} /> {scanning ? t("Scanning…") : t("Scan MCP")}
          </button>
          <button className={styles.primaryButton} type="button" onClick={openAddDialog}>
            <AppIcon name="plus" size={15} /> {t("Add MCP")}
          </button>
        </div>
      </div>

      {notice && <div role="status" className={`${styles.notice} ${notice.kind === "error" ? styles.noticeError : styles.noticeSuccess}`}>{notice.text}</div>}

      {!loading && connections.length === 0 && (
        <button className={styles.emptyAction} type="button" onClick={openAddDialog}>
          <AppIcon name="connection" size={19} />
          <span><strong>{t("No custom MCPs yet")}</strong><small>{t("Add an MCP server to use it alongside installed Plugins.")}</small></span>
          <AppIcon name="plus" size={15} />
        </button>
      )}

      {connections.length > 0 && (
        <div className={styles.connectionGrid}>
          {connections.map((item) => (
            <article className={styles.connectionCard} key={`${item.target}-${item.deviceId || "cloud"}-${item.server.name}`}>
              <div className={styles.connectionTop}>
                <span className={styles.connectionIcon}><AppIcon name={item.target === "online" ? "connection" : "terminal"} size={17} /></span>
                <div className={styles.connectionCopy}>
                  <strong>{item.server.name}</strong>
                  <span>{serverSummary(item.server, t("Remote MCP"))}</span>
                </div>
                <span className={styles.status} data-state={item.state}><i />{t(statusLabel(item.state))}</span>
              </div>
              <div className={styles.connectionMeta}>
                <span>{item.target === "online" ? t("CodeLocal") : deviceLabel(devices, t("Unknown device"), item.deviceId)}</span>
                <span>{t("{count} tools", { count: item.toolCount })}</span>
              </div>
              {item.lastError && <p className={styles.errorText}>{item.lastError}</p>}
              <div className={styles.connectionActions}>
                {item.configEditable && <button type="button" disabled={saving} onClick={() => openEditDialog(item)}>{t("Edit JSON")}</button>}
                <button type="button" disabled={saving} onClick={() => void removeConnection(item)}>{t("Remove")}</button>
              </div>
            </article>
          ))}
        </div>
      )}

      {localConfigGroups.length > 0 && (
        <section className={styles.configPanel}>
          <div className={styles.configHeader}>
            <div>
              <strong>{t("Local MCP Config")}</strong>
              <span>{t("Only MCPs discovered from this device are shown here. No sample data.")}</span>
            </div>
          </div>
          <div className={styles.configList}>
            {localConfigGroups.map((group) => (
              <article className={styles.configCard} key={group.deviceId}>
                <header>
                  <div>
                    <strong>{deviceLabel(devices, t("Unknown device"), group.deviceId)}</strong>
                    <span>{t("{count} MCP servers", { count: group.count })}</span>
                  </div>
                  <button className={styles.secondaryButton} type="button" onClick={() => void copyConfig(group.json)}>
                    <AppIcon name="copy" size={14} /> {t("Copy JSON")}
                  </button>
                </header>
                <pre>{group.json}</pre>
              </article>
            ))}
          </div>
        </section>
      )}

      {dialogOpen && (
        <div className={styles.backdrop} role="presentation" onMouseDown={(event) => { if (event.currentTarget === event.target && !saving) setDialogOpen(false); }}>
          <section className={styles.modal} role="dialog" aria-modal="true" aria-labelledby="add-mcp-title">
            <header className={styles.modalHeader}>
              <div><h2 id="add-mcp-title">{t(editing ? "Edit MCP JSON" : "Add MCP")}</h2><p>{t(editing ? "Edit the runtime JSON for this MCP. The server name stays unchanged." : "Choose where it runs, then add a URL, command, or paste JSON.")}</p></div>
              <button className={styles.iconButton} type="button" disabled={saving} onClick={() => setDialogOpen(false)} aria-label={t("Close")}><AppIcon name="close" size={17} /></button>
            </header>

            {!editing && <div className={styles.targetGrid} role="group" aria-label={t("Where should this MCP run?")}>
              <button type="button" data-active={target === "online" || undefined} className={styles.targetCard} onClick={() => selectTarget("online")}>
                <AppIcon name="connection" size={19} /><strong>{t("Online")}</strong><span>{t("Works even when your computer is off")}</span>
              </button>
              <button type="button" data-active={target === "local" || undefined} className={styles.targetCard} onClick={() => selectTarget("local")}>
                <AppIcon name="device" size={19} /><strong>{t("On my device")}</strong><span>{t("Runs through the CodeLocal client")}</span>
              </button>
            </div>}

            {target === "local" && (
              <label className={styles.field}>
                <span>{t("Device")}</span>
                <select value={deviceId} disabled={Boolean(editing)} onChange={(event) => setDeviceId(event.target.value)}>
                  {devices.length === 0 && <option value="">{t("No paired devices")}</option>}
                  {devices.map((device) => <option key={device.deviceId} value={device.deviceId}>{device.deviceName} · {t(device.status === "online" ? "Online" : "Offline")}</option>)}
                </select>
              </label>
            )}

            {!editing && <div className={styles.modeTabs} role="tablist" aria-label={t("MCP setup method")}>
              <button type="button" role="tab" aria-selected={setupMode === "quick"} data-active={setupMode === "quick" || undefined} onClick={() => setSetupMode("quick")}>{t("Quick setup")}</button>
              <button type="button" role="tab" aria-selected={setupMode === "json"} data-active={setupMode === "json" || undefined} onClick={() => setSetupMode("json")}>{t("Paste JSON")}</button>
            </div>}

            {setupMode === "quick" ? (
              <div className={styles.formStack}>
                <label className={styles.field}><span>{t("Name")}</span><input value={name} onChange={(event) => setName(event.target.value)} autoComplete="off" /></label>
                {target === "online" ? (
                  <>
                    <label className={styles.field}><span>{t("MCP URL")}</span><input value={url} onChange={(event) => setURL(event.target.value)} inputMode="url" autoComplete="off" /></label>
                    <label className={styles.field}><span>{t("Bearer token")} <em>{t("Optional")}</em></span><input type="password" value={bearerToken} onChange={(event) => setBearerToken(event.target.value)} placeholder={t("Leave blank if this MCP does not need a token")} autoComplete="new-password" /></label>
                  </>
                ) : (
                  <>
                    <label className={styles.field}><span>{t("Command")}</span><input value={command} onChange={(event) => setCommand(event.target.value)} autoComplete="off" /></label>
                    <label className={styles.field}><span>{t("Arguments")} <em>{t("One per line")}</em></span><textarea value={argsText} onChange={(event) => setArgsText(event.target.value)} spellCheck={false} /></label>
                  </>
                )}
              </div>
            ) : (
              <div className={styles.formStack}>
                <label className={styles.field}><span>{t("MCP JSON")}</span><textarea className={styles.codeInput} value={config} onChange={(event) => setConfig(event.target.value)} spellCheck={false} /></label>
                {refs.length > 0 && (
                  <div className={styles.secretPanel}>
                    <div><strong>{t("Secrets referenced by this config")}</strong><p>{target === "local" ? t("Leave blank to use the environment variable already configured on this device.") : t("Values are encrypted and never written into the MCP JSON.")}</p></div>
                    {refs.map((ref) => <label className={styles.field} key={ref}><span>{ref}</span><input type="password" autoComplete="new-password" value={secrets[ref] ?? ""} onChange={(event) => setSecrets((current) => ({ ...current, [ref]: event.target.value }))} /></label>)}
                  </div>
                )}
              </div>
            )}

            <div className={styles.reviewRow}>
              <AppIcon name="shield" size={16} />
              <span>{target === "online" ? t("CodeLocal tests the MCP before marking it Ready.") : t("CodeLocal saves it to the selected device and probes it before marking it Ready.")}</span>
            </div>

            <footer className={styles.modalFooter}>
              <button className={styles.secondaryButton} type="button" disabled={saving} onClick={() => setDialogOpen(false)}>{t("Cancel")}</button>
              <button className={styles.primaryButton} type="button" disabled={saving || (target === "local" && !deviceId)} onClick={() => void addConnection()}>{saving ? t("Testing…") : t(editing ? "Save & reload MCP" : "Test & add MCP")}</button>
            </footer>
          </section>
        </div>
      )}
    </section>
  );
}
