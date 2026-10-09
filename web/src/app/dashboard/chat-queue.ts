export type QueuedChatPrompt = { threadId: string; content: string };
export type ChatRunOutcome = "idle" | "running" | "done" | "blocked";

// A client-side queue may dispatch only after a successful run in the same
// immutable thread. Approval-required, aborted and failed runs stay blocked.
export function shouldDispatchQueuedPrompt(args: {
  queue: QueuedChatPrompt | null;
  activeThreadId: string | null;
  busy: boolean;
  runOutcome: ChatRunOutcome;
}): boolean {
  const { queue, activeThreadId, busy, runOutcome } = args;
  return Boolean(!busy && runOutcome === "done" && queue?.threadId &&
    queue.threadId === activeThreadId && queue.content.trim());
}
