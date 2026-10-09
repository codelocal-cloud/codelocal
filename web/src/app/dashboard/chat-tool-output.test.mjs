import assert from "node:assert/strict";
import { chatToolOutput } from "./chat-tool-output.ts";

const diff = "diff --git a/app.ts b/app.ts\n--- a/app.ts\n+++ b/app.ts\n@@ -1 +1 @@\n-old\n+new\n";
const result = chatToolOutput("review_project_diff", JSON.stringify({ ok: true, result: { diff } }));
assert.equal(result.kind, "diff");
assert.equal(result.files, 1);
assert.equal(result.additions, 1);
assert.equal(result.deletions, 1);
assert.equal(result.text, diff);

assert.equal(chatToolOutput("verify_project_changes", JSON.stringify({ result: { gitDiff: { diff } } })).kind, "diff");
assert.deepEqual(chatToolOutput("review_project_diff", "{bad").kind, "none");
assert.equal(chatToolOutput("run_project_command", JSON.stringify({result: {stdout: {text: "build ok"}, stderr: {text: "warning"}}})).text, "build ok\nstderr:\nwarning");
assert.equal(chatToolOutput("get_project_git_status", JSON.stringify({result: {output: "clean"}})).text, "clean");
assert.equal(chatToolOutput("review_project_diff", JSON.stringify({result:{diff:"x".repeat(100_000)}})).truncated, true);
assert.equal(chatToolOutput("review_project_diff", JSON.stringify({result:{diff, truncated:true}})).truncated, true);
console.log("Chat tool diff/log projection checks passed.");
