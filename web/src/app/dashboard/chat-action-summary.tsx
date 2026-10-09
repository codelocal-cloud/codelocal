"use client";

import { AppIcon } from "./app-icon";
import { chatToolOutput } from "./chat-tool-output";
import { PluginApproval } from "./plugin-approval";
import { useTranslations } from "@/lib/i18n/provider";
import type { MessageKey } from "@/lib/i18n/messages";
import styles from "./chat-action-summary.module.css";

export type ChatToolCall = {
  id: string;
  name: string;
  arguments: string;
  result?: string;
  durationMs?: number;
  status: "running" | "done" | "error" | "approval_required";
};

const actionLabels: Partial<Record<string, MessageKey>> = {
  list_workspaces: "View workspaces",
  list_devices: "Check devices",
  search_project_brain: "Search Project Brain",
  get_workspace_detail: "Read project",
  list_project_files: "View source code",
  read_project_file: "Read file",
  search_project_code: "Search code",
  edit_project_file: "Edit code",
  write_project_file: "Update file",
  apply_project_patch: "Apply changes",
  run_project_command: "Run command",
  poll_project_command: "Wait for command",
  verify_project_changes: "Verify changes",
};

function actionLabel(name: string, t: ReturnType<typeof useTranslations>["t"]) {
  if (name === "get_project_git_status") return "Check Git status";
  if (name === "review_project_diff") return "Review Git changes";
  const key = actionLabels[name];
  return key ? t(key) : name.replaceAll("_", " ");
}

function durationLabel(durationMs: number) {
  if (durationMs < 1000) return `${durationMs}ms`;
  if (durationMs < 60_000) return `${(durationMs / 1000).toFixed(durationMs < 10_000 ? 1 : 0)}s`;
  const minutes = Math.floor(durationMs / 60_000);
  const seconds = Math.round((durationMs % 60_000) / 1000);
  return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`;
}

function summaryState(actions: ChatToolCall[]) {
  if (actions.some((action) => action.status === "approval_required")) return "approval";
  if (actions.some((action) => action.status === "running")) return "running";
  if (actions.some((action) => action.status === "error")) return "error";
  return "done";
}

function summaryLabel(actions: ChatToolCall[], state: ReturnType<typeof summaryState>, t: ReturnType<typeof useTranslations>["t"]) {
  if (state === "approval") return t("Waiting for approval");
  if (state === "running") return t("{count} steps running", { count: actions.filter((action) => action.status === "running").length });
  if (state === "error") {
    const failed = actions.filter((action) => action.status === "error").length;
    return t("{done} complete · {failed} errors", { done: actions.length - failed, failed });
  }
  return t("{count} steps completed", { count: actions.length });
}

export function ChatActionSummary({ actions, busy, onApprove }: { actions: ChatToolCall[]; busy: boolean; onApprove: (mode: "smart" | "full") => void }) {
  const { t } = useTranslations();
  const state = summaryState(actions);
  const durationMs = actions.reduce((total, action) => total + Math.max(0, action.durationMs || 0), 0);
  const approvals = actions.filter((action) => action.status === "approval_required");
  const stateLabel = state === "approval" ? t("Approval required") : state === "error" ? t("Completed with errors") : state === "running" ? t("Running") : t("Completed");
  const label = summaryLabel(actions, state, t);

  return (
    <div className={styles.actionGroup} data-state={state}>
      <details className={styles.disclosure}>
        <summary role="button" aria-label={`${label}${durationMs > 0 ? `, ${durationLabel(durationMs)}` : ""}`}>
          <span className={styles.stateIcon} aria-hidden="true">{state === "done" ? <AppIcon name="check" size={13} /> : <i />}</span>
          <span className={styles.summaryCopy} aria-live="polite">
            <strong>{label}</strong>
            <small>{stateLabel}{durationMs > 0 ? ` · ${durationLabel(durationMs)}` : ""}</small>
          </span>
          <AppIcon className={styles.chevron} name="chevron-right" size={14} />
        </summary>
        <ol className={styles.timeline}>
          {actions.map((action) => {
            const output = chatToolOutput(action.name, action.result);
            return (
            <li key={action.id} data-state={action.status}>
              <span className={styles.timelineDot} aria-hidden="true" />
              <div className={styles.actionCopy}>
                <strong>{actionLabel(action.name, t)}</strong>
                <small>{action.status === "approval_required" ? t("Needs permission") : action.status === "error" ? t("Error") : action.status === "running" ? t("Running") : t("Completed")}{action.durationMs ? ` · ${durationLabel(action.durationMs)}` : ""}</small>
                {output.kind !== "none" ? (
                  <details className={styles.toolOutput}>
                    <summary>{output.kind === "diff" ? `Review diff · ${output.files} files · +${output.additions} / -${output.deletions}` : "View command output"}</summary>
                    {output.text ? (
                      <pre aria-label={output.kind === "diff" ? "Git unified diff" : "Command output"}>
                        {output.kind === "diff"
                          ? output.text.split("\n").map((line, index) => (
                              <span key={index} data-diff-line={line.startsWith("+") && !line.startsWith("+++") ? "add" : line.startsWith("-") && !line.startsWith("---") ? "remove" : line.startsWith("@@") ? "hunk" : "context"}>{line}{"\n"}</span>
                            ))
                          : output.text}
                      </pre>
                    ) : <small>No changes or output.</small>}
                    {output.truncated ? <small>Output truncated for display. Request a narrower file diff to review all changes.</small> : null}
                  </details>
                ) : null}
                {action.arguments || action.result ? (
                  <details className={styles.actionData}>
                    <summary>{t("Details")}</summary>
                    {action.arguments ? <code>{action.arguments}</code> : null}
                    {action.result ? <code>{action.result.length > 800 ? `${action.result.slice(0, 800)}…` : action.result}</code> : null}
                  </details>
                ) : null}
              </div>
            </li>
            );
          })}
        </ol>
      </details>
      {approvals.map((action) => {
        if (action.name === "call_plugin_tool") {
          let request: { approvalId?: unknown; plugin?: unknown; connection?: unknown; tool?: unknown; arguments?: unknown } = {};
          try { request = JSON.parse(action.result || "{}"); } catch { /* Missing approval data must not enable execution. */ }
          if (typeof request.approvalId !== "string" || !/^[a-f0-9]{48}$/.test(request.approvalId)) return null;
          return <PluginApproval key={`${action.id}-approval`} id={request.approvalId} busy={busy} />;
        }
        return <div className={styles.approvalRow} key={`${action.id}-approval`}>
          <span>
            <AppIcon name="shield" size={15} />
            <strong>{actionLabel(action.name, t)}</strong>
            <small>Choose a workspace access mode. This changes project permissions, not just this action.</small>
          </span>
          <div className={styles.approvalChoices}>
            <button type="button" onClick={() => onApprove("smart")} disabled={busy} title="Use guarded Smart access for this workspace">Smart</button>
            <button type="button" onClick={() => {
              if (window.confirm("Full access changes permission for the entire workspace, not only this action. Continue?")) onApprove("full");
            }} disabled={busy} title="Allow all non-blocked workspace actions">Full access…</button>
          </div>
        </div>;
      })}
    </div>
  );
}
