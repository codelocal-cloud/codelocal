import assert from "node:assert/strict";
import { shouldDispatchQueuedPrompt } from "./chat-queue.ts";

const queue = {threadId:"thread-a",content:"Next fix"};
const check = (params) => shouldDispatchQueuedPrompt({queue,activeThreadId:"thread-a",busy:false,runOutcome:"done",...params});
assert.equal(check({}), true);
assert.equal(check({busy:true}), false);
assert.equal(check({runOutcome:"blocked"}), false);
assert.equal(check({runOutcome:"running"}), false);
assert.equal(check({activeThreadId:"thread-b"}), false);
assert.equal(check({queue:null}), false);
assert.equal(check({queue:{threadId:"thread-a",content:"  "}}), false);
console.log("Chat queued-prompt isolation and failure gating checks passed.");
