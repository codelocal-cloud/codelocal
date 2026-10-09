# CodeLocal Web Chat — Coding Agent Experience B
Date: 2026-10-09 · Implementation branch: `feat/web-chat-coding-agent-b` · Base: `main`
Status: in progress; **do not merge into main** without the release gate.

## Goal, actors, boundaries
Build a dependable browser Coding Agent comparable in day-to-day interaction to Codex/Claude Code, while retaining CodeLocal's device-scoped execution, Safe Workspace (default), Live Project, Project Brain, BYOK, and explicit local approvals. Persona A: individual developer troubleshooting a repository; B: developer supervising parallel long-running tasks; C: team reviewer approving patches. Browser has no direct filesystem authority; Go validates auth, thread/workspace ownership, access policy and calls the paired runtime. Never run shell/git from the web process.

## User journeys / BA acceptance cases
1. **New task:** select specific device/project, Safe or Live, model, Ask/Plan/Agent. Chat thread immutably binds workspace; switching project starts a separate thread; offline workspace is reported, not silently substituted.
2. **Inspect/refactor/test:** agent searches with Project Brain/LSP, reads scoped files, proposes edit, executes under local policy, runs verification, reports changed files and tests with honest success/failure. Ask/Plan are read-only.
3. **Review patch:** user can request git status/diff without mutation; view unified diff per file, additions/deletions, no fabricated text, empty state for clean repo, explicit truncation limits for large output. Rejecting a patch must not silently overwrite unrelated changes; future per-hunk accept/reject must require clean scope and conflict checks.
4. **Dangerous action approval:** default Prompt/Smart policy; show precise command/tool and target, allow/deny and optional time/scope bounded approval only after the Go/runtime API explicitly supports it. Never mislabel project-wide Full access as a one-off approval; require confirmation before elevating workspace access.
5. **Queued user input:** while tool execution is active, user can draft and enqueue another instruction without overwriting the running request; queue is thread-bound, visibly removable, and never silently sent to another project. Eventually support steer/interrupt with run IDs and durable state.
6. **Long task:** server stores job/run ID, state transitions queued/running/waiting_approval/succeeded/failed/cancelled, sequence-numbered events and latest checkpoint. Refresh/reconnect must replay state and attach to the same execution, not duplicate side effects. A cancel request must reach the runtime (not only abort the HTTP reader).
7. **Multi-task:** run isolation by workspace and worktree, explicit resource ceilings, separate streaming and history per task, safe git merge only after conflict checks and per-task review. Avoid concurrently mutating the same Live Project checkout.
8. **Network failure:** SSE heartbeats, bounded retry with request idempotency, no two mutating tool invocations for one request ID, resume from checkpoint after browser/tab loss. If result unknown, report unknown, not success.
9. **Attachments:** multi-image and bounded documents/logs with preview/remove, uploaded-file integrity, content-type verification, permission and retention; never replay unauthenticated URLs/keys.
10. **Mobile & accessibility:** virtual keyboard/safe area, keyboard Enter/Shift+Enter desktop, 320px width, focus management, reduced motion, readable long command output, accessible dialogs.
11. **Security:** cross-tenant isolation, CSRF strategy on same-origin writes, per-user rate limits, tool-path sandboxing, secret redaction in chat logs, explicit audit trail. Browser-provided workspace IDs are hints, not authorization.
12. **Review & release:** no production claims without browser-authenticated E2E, Go integration + TypeScript tests, lint/build, security checks and explicit review.

## Implementation checklist (ordered)
### P0 — baseline / guardrails
- [x] Create isolated branch from clean main.
- [x] Replace blocking Chat construction dialog with truthful non-blocking preview indication, retain blockers for non-Chat feature routes.
- [x] Replace one-click Full-access elevation with guarded Smart and explicitly confirmed project-wide Full access. Per-action approval stays pending.
- [x] Make chat-history clear transactional: retain messages until DELETE succeeds, preserve original content on API error.
- [x] Add regression tests for Git output projection, queue isolation and Go runtime-tool mapping/workspace lock.
- [x] Run targeted Go/API + web typecheck/lint/build and security checks.

### P1 — coding workflow
- [x] Expose read-only git status/diff via existing paired runtime (not shell proxy); honor thread-bound workspace.
- [x] Render structured diff and terminal outputs in tool history; cap presentation and display truncation clearly.
- [x] Add a thread-scoped, one-item *client-side* prompt queue; preserve user drafts and guard on failed/blocked runs. Durable queue remains TODO.
- [ ] Build a dedicated diff panel with file summary; per-hunk approve/revert requires backend transactional change API.
- [ ] Persist task-run state with server job lifecycle, event replay and cancellation semantics; upgrade from browser-SSE-lifetime dependency.
- [ ] Introduce Git worktree-per-task / repository lock arbitration, with cleanup and conflict detection.
- [ ] Parallel run supervisor: queue ordering, pause/resume/steer and safe maximum concurrency.

### P2 — productivity / observability
- [ ] Search/select files with @ references and precise context citation.
- [ ] Multi-attachment support with validated upload type/size and retention rules.
- [ ] Model/token/context usage per thread and provider, including failure accounting.
- [ ] Dedicated test failure panes and provenance links for changed files.
- [ ] Project-level run dashboards, audit, mobile enhancements.

## Non-negotiable release gate
- All changed frontend: `npm run lint --prefix web`, `npm run typecheck --prefix web`, `npm run test:ui --prefix web`, `npm run build --prefix web`, `npm audit --omit=dev --prefix web`.
- Backend contract modifications: `go test ./internal/cloudserver` plus broader repository checks if shared layer touched.
- Browser E2E using authenticated test account: new task, read/edit/verify, denied approval, full access explicit consent, stream reconnection, two concurrent task attempts, refresh mid-run, destructive command blocked, mobile layout.
- Review `git diff --check`, current branch, secrets and unrelated changes. Any failing check is recorded; **no merge**.
- Do not claim production ready until the above is evidenced.

## Implementation status — first increment
- **Implemented:** non-blocking Chat preview, non-optimistic clear, guarded Smart + explicitly confirmed project-wide Full access, runtime Git status/diff read-only tools, diff/terminal output projection, thread-scoped single pending prompt gated on successful completion, and runtime tool workspace lock for bound tasks.
- **Security:** updated web Next.js from 16.3.4 to 16.4.0; refreshed transitive dependencies. Production-only npm audit reports zero vulnerabilities; dev-tool dependency chain (ESLint micromatch/braces) still reports high-severity advisories. Do not force-downgrade ESLint/Next to suppress the finding.
- **Verified:** Go cloudserver tests, web lint (one pre-existing social-card image warning), TypeScript typecheck, UI tests including new Git diff/queue helpers, Next.js production build, git diff --check. Audit production dependencies: 0 vulnerabilities.
- **Release blockers:** authenticated browser E2E not available in this session; durable server-owned task jobs/replay/cancel, concurrent worktrees, per-action scoped approvals and full diff revert UI remain incomplete. No production deployment or merge.
- **Concurrent workspace changes:** `docs/plans/product/dashboard-chat.md` and `docs/plans/product/GOAT_AUTONOMOUS_MODE.md` appeared from separate work, exclude them from this increment's staging/commit.

## Implementation tracking
- Source audit found `/chat` has blocking ConstructionNotice, history clear mutates state before server confirmation, non-plugin approval offers Full access, runtime supports `git_status` and `git_diff` but Chat wrapper does not currently offer them.
- First increment limits scope to honest UX and reuse existing read-only tools. Durable jobs/worktree isolation are separate follow-on increments requiring transport + persistence design, not simulated client-side.
