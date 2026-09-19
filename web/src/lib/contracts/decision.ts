export type DecisionMode = "off" | "shadow" | "active";

export type DecisionFeatureKey =
  | "route"
  | "context"
  | "output"
  | "model"
  | "review"
  | "brain"
  | "computer";

export type DecisionStatusResource = {
  mode: DecisionMode;
  provider: string;
  primaryReady: boolean;
  managed: boolean;
  features: Record<DecisionFeatureKey, boolean>;
};

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function isDecisionStatusResource(value: unknown): value is DecisionStatusResource {
  if (!record(value) || !record(value.features)) return false;
  const features = value.features;
  if (value.mode !== "off" && value.mode !== "shadow" && value.mode !== "active") return false;
  if (typeof value.provider !== "string" || typeof value.primaryReady !== "boolean" || typeof value.managed !== "boolean") return false;
  return ["route", "context", "output", "model", "review", "brain", "computer"].every(
    (key) => typeof features[key] === "boolean",
  );
}
