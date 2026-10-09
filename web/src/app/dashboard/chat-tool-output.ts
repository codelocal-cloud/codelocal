// Display-only projection of authenticated CodeLocal runtime tool results.
// The backend remains authoritative; never infer success from a non-empty log.
export type ChatToolOutput = {
  kind: "diff" | "terminal" | "none";
  text: string;
  truncated: boolean;
  files: number;
  additions: number;
  deletions: number;
};

const MAX_DISPLAY_CHARS = 80_000;
const MAX_DISPLAY_LINES = 1_500;

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown> : null;
}

function outputText(value: unknown): string {
  if (typeof value === "string") return value;
  const obj = record(value);
  return obj && typeof obj.text === "string" ? obj.text : "";
}

function limitDisplay(value: string): { text: string; truncated: boolean } {
  const lines = value.split("\n");
  const lineLimit = lines.length > MAX_DISPLAY_LINES;
  let text = lineLimit ? lines.slice(0, MAX_DISPLAY_LINES).join("\n") : value;
  const lengthLimit = text.length > MAX_DISPLAY_CHARS;
  if (lengthLimit) text = text.slice(0, MAX_DISPLAY_CHARS);
  return { text, truncated: lineLimit || lengthLimit };
}

export function chatToolOutput(name: string, raw: string | undefined): ChatToolOutput {
  const empty: ChatToolOutput = { kind: "none", text: "", truncated: false, files: 0, additions: 0, deletions: 0 };
  if (!raw) return empty;
  let payload: unknown;
  try { payload = JSON.parse(raw); } catch { return empty; }
  const wrapper = record(payload);
  const result = record(wrapper?.result) ?? wrapper;
  if (!result) return empty;

  if (name === "review_project_diff" || name === "verify_project_changes") {
    const gitDiff = record(result.gitDiff);
    const full = outputText(result.diff) || outputText(gitDiff?.diff);
    if (!full) return { ...empty, kind: "diff" };
    const files = (full.match(/^diff --git /gm) ?? []).length;
    const additions = (full.match(/^\+(?!\+\+)/gm) ?? []).length;
    const deletions = (full.match(/^-(?!--)/gm) ?? []).length;
    const display = limitDisplay(full);
    return { kind: "diff", ...display, truncated: display.truncated || result.truncated === true, files, additions, deletions };
  }

  if (name === "run_project_command" || name === "poll_project_command" || name === "get_project_git_status") {
    const stdout = outputText(result.stdout) || outputText(result.output);
    const stderr = outputText(result.stderr);
    const combined = [stdout, stderr ? `stderr:\n${stderr}` : ""].filter(Boolean).join("\n");
    return { ...empty, kind: "terminal", ...limitDisplay(combined) };
  }
  return empty;
}
