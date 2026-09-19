"use client";

import { useState } from "react";
import { isAccountResource } from "@/lib/contracts/account";
import { isDecisionStatusResource, type DecisionFeatureKey } from "@/lib/contracts/decision";
import { isRuntimeSettingsResource } from "@/lib/contracts/runtime-settings";
import { useTranslations } from "@/lib/i18n/provider";
import type { MessageKey } from "@/lib/i18n/messages";
import { AppIcon } from "../app-icon";
import { DashboardResourceFeedback } from "../dashboard-resource-feedback";
import { useDashboardResource } from "../use-dashboard-resource";
import styles from "./decision-settings.module.css";

const MODE_KEY = "CODELOCAL_DECISION_MODE";

const features: Array<{ key: DecisionFeatureKey; config: string; label: MessageKey; detail: MessageKey }> = [
  { key: "route", config: "CODELOCAL_DECISION_ROUTE", label: "Task routing", detail: "Choose the best execution lane for each task." },
  { key: "context", config: "CODELOCAL_DECISION_CONTEXT", label: "Context optimization", detail: "Rank the most relevant files before reasoning." },
  { key: "output", config: "CODELOCAL_DECISION_OUTPUT", label: "Tool output filtering", detail: "Identify large tool outputs that can be compacted safely." },
  { key: "model", config: "CODELOCAL_DECISION_MODEL", label: "Model routing", detail: "Compare configured model routes without exposing chat content." },
  { key: "review", config: "CODELOCAL_DECISION_REVIEW", label: "Review triage", detail: "Focus review effort on higher-risk changed files." },
  { key: "brain", config: "CODELOCAL_DECISION_BRAIN", label: "Project Brain routing", detail: "Rank relevant Brain rules without weakening mandatory rules." },
  { key: "computer", config: "CODELOCAL_DECISION_COMPUTER", label: "Computer decisions", detail: "Evaluate semantic desktop targets before bounded actions." },
];

function enabled(values: Record<string, string>, key: string, fallback: boolean) {
  const value = values[key]?.trim().toLowerCase();
  if (!value) return fallback;
  return !["0", "false", "off", "disabled", "no"].includes(value);
}

export function DecisionSettingsLive() {
  const { t } = useTranslations();
  const [saving, setSaving] = useState("");
  const [feedback, setFeedback] = useState<"success" | "error" | null>(null);
  const account = useDashboardResource("/api/v1/account", isAccountResource);
  const status = useDashboardResource("/api/v1/decision/status", isDecisionStatusResource);
  const settings = useDashboardResource("/api/v1/runtime/settings?scope=global", isRuntimeSettingsResource);

  if (account.state.kind !== "ready" || status.state.kind !== "ready" || settings.state.kind !== "ready") {
    const failed = [account, status, settings].find((resource) => resource.state.kind === "error");
    return (
      <section className={styles.panel}>
        <DashboardResourceFeedback
          label="Decision Engine"
          {...(failed && failed.state.kind === "error"
            ? { kind: "error" as const, message: failed.state.message, onRetry: () => { account.retry(); status.retry(); settings.retry(); } }
            : { kind: "loading" as const })}
        />
      </section>
    );
  }

  const csrf = account.state.value.csrf;
  const current = status.state.value;
  const values = settings.state.value.layer.values ?? {};
  const automatic = values[MODE_KEY]?.trim().toLowerCase() !== "off";

  async function setConfig(key: string, value: string, action = "set") {
    if (!csrf) return;
    setSaving(key);
    setFeedback(null);
    try {
      const body = new URLSearchParams({ csrf, scope: "global", key, value, action });
      const response = await fetch("/api/v1/runtime/settings/config", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8" },
        body,
      });
      if (!response.ok) throw new Error("decision setting rejected");
      setFeedback("success");
      settings.retry();
      status.retry();
    } catch {
      setFeedback("error");
    } finally {
      setSaving("");
    }
  }

  const providerState = current.mode === "off"
    ? t("Off")
    : current.primaryReady
      ? t("Healthy")
      : t("Fallback");

  return (
    <section className={styles.panel} aria-label={t("AI Optimization")}>
      <header className={styles.header}>
        <span className={styles.icon}><AppIcon name="brain" size={18} /></span>
        <div className={styles.copy}>
          <div className={styles.titleLine}>
            <h2>{t("AI Optimization")}</h2>
            <span data-state={current.primaryReady ? "healthy" : current.mode === "off" ? "off" : "fallback"}>{providerState}</span>
          </div>
          <p>{t("Small decisions are optimized without changing your security and approval rules.")}</p>
        </div>
      </header>

      <div className={styles.summary}>
        <div>
          <span>{t("Decision Engine")}</span>
          <strong>{automatic ? t("Automatic") : t("Off")}</strong>
        </div>
        <div>
          <span>{t("Provider")}</span>
          <strong>{t("CodeLocal Managed")}</strong>
        </div>
        <div>
          <span>{t("Server rollout")}</span>
          <strong>{current.mode}</strong>
        </div>
      </div>

      <div className={styles.modeChoices}>
        <button type="button" data-active={automatic} disabled={Boolean(saving)} onClick={() => void setConfig(MODE_KEY, "", "delete")}>
          <strong>{t("Automatic")}</strong>
          <small>{t("Follow the safe rollout managed by CodeLocal.")}</small>
        </button>
        <button type="button" data-active={!automatic} disabled={Boolean(saving)} onClick={() => void setConfig(MODE_KEY, "off")}>
          <strong>{t("Off")}</strong>
          <small>{t("Use the existing deterministic CodeLocal behavior only.")}</small>
        </button>
      </div>

      <div className={styles.features}>
        {features.map((feature) => {
          const isEnabled = automatic && enabled(values, feature.config, current.features[feature.key]);
          return (
            <button
              type="button"
              className={styles.feature}
              key={feature.key}
              disabled={!automatic || Boolean(saving)}
              aria-pressed={isEnabled}
              onClick={() => void setConfig(feature.config, isEnabled ? "off" : "on")}
            >
              <span>
                <strong>{t(feature.label)}</strong>
                <small>{t(feature.detail)}</small>
              </span>
              <span className={styles.toggle} data-active={isEnabled}><i /></span>
            </button>
          );
        })}
      </div>

      <footer className={styles.footer}>
        <span>{t("No Jev key or extra installation is required on user devices.")}</span>
        {feedback === "success" ? <strong>{t("Saved")}</strong> : feedback === "error" ? <strong data-error="true">{t("CodeLocal could not be reached.")}</strong> : null}
      </footer>
    </section>
  );
}
