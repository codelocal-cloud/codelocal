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
type PanelTab = "mcps" | "config";
type ConfigChangeSet = {
  added: string[];
  updated: string[];
  removed: string[];
  error?: string;
};

function stableJSON(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>)
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, item]) => `${JSON.stringify(key)}:${stableJSON(item)}`);
    return `{${entries.join(",")}}`;
  }
  return JSON.stringify(value);
}

function parseMCPConfig(raw: string): Record<string, Record<string, unknown>> {
  const parsed = JSON.parse(raw) as unknown;
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error("invalid");
  const root = parsed as Record<string, unknown>;
  if (Object.keys(root).some((key) => key !== "mcpServers")) throw new Error("invalid");
  const servers = root.mcpServers;
  if (!servers || typeof servers !== "object" || Array.isArray(servers)) throw new Error("invalid");
  const result: Record<string, Record<string, unknown>> = {};
  for (const [name, config] of Object.entries(servers as Record<string, unknown>)) {
    if (!name.trim() || !config || typeof config !== "object" || Array.isArray(config)) throw new Error("invalid");
    result[name] = config as Record<string, unknown>;
  }
  return result;
}

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
  const [activeTab, setActiveTab] = useState<PanelTab>("mcps");
  const [showJSONExample, setShowJSONExample] = useState(false);
  const [configDeviceId, setConfigDeviceId] = useState("");
  const [configDraft, setConfigDraft] = useState("");
  const [applyingConfig, setApplyingConfig] = useState(false);

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

  const selectedConfigGroup = useMemo(
    () => localConfigGroups.find((group) => group.deviceId === configDeviceId) ?? localConfigGroups[0],
    [configDeviceId, localConfigGroups],
  );

  const configChanges = useMemo<ConfigChangeSet>(() => {
    if (!selectedConfigGroup || !configDraft.trim()) return { added: [], updated: [], removed: [] };
    try {
      const current = parseMCPConfig(selectedConfigGroup.json);
      const next = parseMCPConfig(configDraft);
      const added = Object.keys(next).filter((key) => !(key in current)).sort();
      const removed = Object.keys(current).filter((key) => !(key in next)).sort();
      const updated = Object.keys(next)
        .filter((key) => key in current && stableJSON(next[key]) !== stableJSON(current[key]))
        .sort();
      return { added, updated, removed };
    } catch {
      return { added: [], updated: [], removed: [], error: "invalid" };
    }
  }, [configDraft, selectedConfigGroup]);

  const configChangeCount = configChanges.added.length + configChanges.updated.length + configChanges.removed.length;
  const hasConfigChanges = !configChanges.error && configChangeCount > 0;

  function openConfigTab() {
    const selected = localConfigGroups.find((group) => group.deviceId === configDeviceId) ?? localConfigGroups[0];
    setActiveTab("config");
    if (!selected) return;
    if (configDeviceId !== selected.deviceId || !configDraft) {
      setConfigDeviceId(selected.deviceId);
      setConfigDraft(selected.json);
    }
  }

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
    setShowJSONExample(false);
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
    setConfig(JSON.stringify(serverConfig(item), null, 2));
    setSecrets({});
    setShowJSONExample(false);
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

  function selectConfigDevice(nextDeviceId: string) {
    const group = localConfigGroups.find((item) => item.deviceId === nextDeviceId);
    setConfigDeviceId(nextDeviceId);
    setConfigDraft(group?.json ?? "");
    setNotice(null);
  }

  async function applyConfigChanges() {
    if (!account?.csrf || !selectedConfigGroup || configChanges.error) return;
    const changedNames = [...configChanges.added, ...configChanges.updated];
    if (changedNames.length === 0 && configChanges.removed.length === 0) return;

    setApplyingConfig(true);
    setNotice(null);
    try {
      const nextServers = parseMCPConfig(configDraft);
      for (const serverName of changedNames) {
        const response = await fetch(`/api/v1/mcp/connections/local/${encodeURIComponent(serverName)}`, {
          method: "PUT",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": account.csrf },
          body: JSON.stringify({
            target: "local",
            deviceId: selectedConfigGroup.deviceId,
            config: JSON.stringify({ mcpServers: { [serverName]: nextServers[serverName] } }, null, 2),
            secrets: {},
          }),
        });
        const payload = await response.json().catch(() => ({})) as { detail?: string; error?: string };
        if (!response.ok) throw new Error(payload.detail || payload.error || t("Could not add this MCP."));
      }

      for (const serverName of configChanges.removed) {
        const response = await fetch(
          `/api/v1/mcp/connections/local/${encodeURIComponent(serverName)}?deviceId=${encodeURIComponent(selectedConfigGroup.deviceId)}`,
          {
            method: "DELETE",
            credentials: "same-origin",
            headers: { "X-CSRF-Token": account.csrf },
          },
        );
        const payload = await response.json().catch(() => ({})) as { detail?: string; error?: string };
        if (!response.ok) throw new Error(payload.detail || payload.error || t("Could not remove this MCP."));
      }

      await refresh();
      setNotice({ kind: "success", text: t("MCP config updated.") });
    } catch (error) {
      await refresh().catch(() => undefined);
      setNotice({ kind: "error", text: error instanceof Error ? error.message : t("MCP connections are temporarily unavailable.") });
    } finally {
      setApplyingConfig(false);
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

      const serverName = editing?.server.name ?? normalizedName(name);
      if (!serverName) throw new Error(t("Enter a name for this MCP."));

      let requestConfig = prepared.config;
      if (setupMode === "json") {
        let parsed: unknown;
        try {
          parsed = JSON.parse(prepared.config);
        } catch {
          throw new Error(t("MCP JSON is invalid."));
        }
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
          throw new Error(t("MCP JSON is invalid."));
        }
        const singleServerConfig = parsed as Record<string, unknown>;
        if ("mcpServers" in singleServerConfig || "servers" in singleServerConfig) {
          throw new Error(t("MCP JSON is invalid."));
        }
        requestConfig = JSON.stringify({ mcpServers: { [serverName]: singleServerConfig } }, null, 2);
      }

      const requestTarget = editing?.target ?? target;
      const endpoint = `/api/v1/mcp/connections/${encodeURIComponent(requestTarget)}/${encodeURIComponent(serverName)}`;
      const response = await fetch(endpoint, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": account.csrf },
        body: JSON.stringify({
          target: requestTarget,
          deviceId: requestTarget === "local" ? (editing?.deviceId ?? deviceId) : undefined,
          config: requestConfig,
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

      <div className={styles.panelTabs} role="tablist" aria-label={t("Custom MCP")}>
        <button type="button" role="tab" aria-selected={activeTab === "mcps"} data-active={activeTab === "mcps" || undefined} onClick={() => setActiveTab("mcps")}>{t("MCPs")}</button>
        <button type="button" role="tab" aria-selected={activeTab === "config"} data-active={activeTab === "config" || undefined} onClick={openConfigTab}>{t("MCP Config")}</button>
      </div>

      {notice && <div role="status" className={`${styles.notice} ${notice.kind === "error" ? styles.noticeError : styles.noticeSuccess}`}>{notice.text}</div>}

      {activeTab === "mcps" && (
        <>
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
        </>
      )}

      {activeTab === "config" && (
        <section className={styles.configPanel}>
          <div className={styles.configHeader}>
            <div>
              <strong>{t("MCP Config")}</strong>
              <span>{t("Edit all local MCP servers for one device. Review the diff before applying.")}</span>
            </div>
          </div>

          {localConfigGroups.length === 0 ? (
            <div className={styles.configEmpty}>
              <AppIcon name="terminal" size={18} />
              <div><strong>{t("No local MCP config yet")}</strong><span>{t("Pair or scan a device to manage its full MCP config.")}</span></div>
            </div>
          ) : (
            <div className={styles.configEditorCard}>
              <div className={styles.configToolbar}>
                <label className={styles.compactField}>
                  <span>{t("Device")}</span>
                  <select value={selectedConfigGroup?.deviceId ?? ""} onChange={(event) => selectConfigDevice(event.target.value)}>
                    {localConfigGroups.map((group) => <option key={group.deviceId} value={group.deviceId}>{deviceLabel(devices, t("Unknown device"), group.deviceId)}</option>)}
                  </select>
                </label>
                <button className={styles.secondaryButton} type="button" onClick={() => void copyConfig(configDraft)}><AppIcon name="copy" size={14} /> {t("Copy JSON")}</button>
              </div>

              <textarea className={styles.fullConfigEditor} value={configDraft} onChange={(event) => setConfigDraft(event.target.value)} spellCheck={false} />

              {configChanges.error ? (
                <div className={styles.configError}>{t("Invalid MCP config.")}</div>
              ) : (
                <div className={styles.diffPanel}>
                  <div className={styles.diffHeader}><strong>{t("Changes")}</strong><span>{configChangeCount}</span></div>
                  {configChangeCount === 0 ? <p>{t("No changes")}</p> : (
                    <div className={styles.diffList}>
                      {configChanges.added.map((item) => <span key={`add-${item}`} data-kind="add">+ {item}</span>)}
                      {configChanges.updated.map((item) => <span key={`update-${item}`} data-kind="update">~ {item}</span>)}
                      {configChanges.removed.map((item) => <span key={`remove-${item}`} data-kind="remove">− {item}</span>)}
                    </div>
                  )}
                </div>
              )}

              <div className={styles.configFooter}>
                <button className={styles.secondaryButton} type="button" disabled={applyingConfig || !selectedConfigGroup} onClick={() => selectedConfigGroup && setConfigDraft(selectedConfigGroup.json)}>{t("Reset")}</button>
                <button className={styles.primaryButton} type="button" disabled={applyingConfig || !hasConfigChanges} onClick={() => void applyConfigChanges()}>{applyingConfig ? t("Applying…") : t("Apply changes")}</button>
              </div>
            </div>
          )}
        </section>
      )}

      {dialogOpen && (
        <div className={styles.backdrop} role="presentation" onMouseDown={(event) => { if (event.currentTarget === event.target && !saving) setDialogOpen(false); }}>
          <section className={styles.modal} role="dialog" aria-modal="true" aria-labelledby="add-mcp-title">
            <header className={styles.modalHeader}>
              <div><h2 id="add-mcp-title">{t(editing ? "Edit MCP" : "Add MCP")}</h2><p>{t(editing ? "Edit only this MCP. Name, target, and device are locked." : "Choose where it runs, then add a URL, command, or paste JSON.")}</p></div>
              <button className={styles.iconButton} type="button" disabled={saving} onClick={() => setDialogOpen(false)} aria-label={t("Close")}><AppIcon name="close" size={17} /></button>
            </header>

            {editing && (
              <div className={styles.lockedContext}>
                <span className={styles.connectionIcon}><AppIcon name={editing.target === "online" ? "connection" : "terminal"} size={16} /></span>
                <div><strong>{editing.server.name}</strong><span>{editing.target === "online" ? t("Online") : `${t("On my device")} · ${deviceLabel(devices, t("Unknown device"), editing.deviceId)}`}</span></div>
                <span className={styles.lockBadge}>🔒</span>
              </div>
            )}

            {!editing && <div className={styles.targetGrid} role="group" aria-label={t("Where should this MCP run?")}>
              <button type="button" data-active={target === "online" || undefined} className={styles.targetCard} onClick={() => selectTarget("online")}>
                <AppIcon name="connection" size={19} /><strong>{t("Online")}</strong><span>{t("Works even when your computer is off")}</span>
              </button>
              <button type="button" data-active={target === "local" || undefined} className={styles.targetCard} onClick={() => selectTarget("local")}>
                <AppIcon name="device" size={19} /><strong>{t("On my device")}</strong><span>{t("Runs through the CodeLocal client")}</span>
              </button>
            </div>}

            {!editing && target === "local" && (
              <label className={styles.field}>
                <span>{t("Device")}</span>
                <select value={deviceId} onChange={(event) => setDeviceId(event.target.value)}>
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
                {!editing && <label className={styles.field}><span>{t("Name")}</span><input value={name} onChange={(event) => setName(event.target.value)} autoComplete="off" /></label>}
                <label className={styles.field}>
                  <span>{t("Server config")}</span>
                  <textarea className={styles.codeInput} value={config} onChange={(event) => setConfig(event.target.value)} spellCheck={false} />
                </label>
                <div className={styles.jsonHelp}>
                  <span>{t("Paste only this MCP's server config. CodeLocal adds the name automatically.")}</span>
                  <button type="button" onClick={() => setShowJSONExample((current) => !current)}>{t(showJSONExample ? "Hide example" : "View example")}</button>
                </div>
                {showJSONExample && (
                  <pre className={styles.exampleCode}>{target === "local" ? `{
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]
}` : `{
  "url": "https://example.com/mcp"
}`}</pre>
                )}
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
