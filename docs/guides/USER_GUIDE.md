# CodeLocal User Guide

## 1. What CodeLocal is

CodeLocal is a universal MCP connection layer for AI coding. It lets ChatGPT, Codex, Claude and other MCP-compatible AI clients and coding agents understand and work with projects that remain on your own computer.

It has three jobs:

1. expose one stable MCP layer to compatible AI clients;
2. give those clients controlled access to local development tools;
3. preserve useful project knowledge across chats, models, clients and machines through Project Brain.

You do not need to upload an entire repository to CodeLocal Cloud.

## 2. Install

Stable channel:

```bash
npm i -g codelocal
```

Beta channel:

```bash
npm i -g codelocal@beta
```

Check the installed version:

```bash
codelocal --version
```

Installing another global channel replaces the currently installed global `codelocal` command. See [Beta Channel](../operations/BETA_CHANNEL.md).

## 3. Connect an MCP-compatible AI client

CodeLocal exposes one remote MCP endpoint:

```text
https://codelocal.cloud/mcp
```

Use that same CodeLocal connection from the AI client or coding agent you prefer, provided it supports the MCP capabilities required by your workflow.

### ChatGPT / Codex

Install CodeLocal from the OpenAI Plugins Directory when available. For reviewer/developer testing, use the custom MCP flow and authenticate with OAuth.

### Other MCP-compatible clients

Add the CodeLocal endpoint as a remote MCP server, choose OAuth when supported, and sign in to the same CodeLocal account used by your paired machines.

Exact MCP features, approval UX and write-action support can vary by client. CodeLocal keeps the same Project Brain, workspace grants and local policy regardless of which compatible client initiates the session.

## 4. Pair one computer

Run:

```bash
codelocal
```

On first use CodeLocal opens a browser approval flow. Approve the device with your CodeLocal account.

A device credential identifies the machine. It can be reviewed and revoked later from the dashboard.

## 5. Authorize a project

Inside a project folder:

```bash
cd /path/to/project
codelocal .
```

This grants that folder to CodeLocal. It does not grant the entire computer.

You can authorize several projects while keeping one machine runtime:

```text
Machine runtime
├── Project A
├── Project B
└── Project C
```

Workspaces sleep until selected by an MCP session.

## 6. Work from your AI client

A typical request can be as simple as:

```text
Use CodeLocal on this project. Understand the relevant code, make the change, and verify it.
```

Under the hood CodeLocal can:

- identify the current project/repository;
- resolve project rules and relevant Project Brain knowledge;
- find relevant code instead of scanning everything;
- read/edit files inside the authorized workspace;
- run Git, tests and terminal commands subject to policy;
- verify the result;
- preserve verified Experience for future work when eligible.

### Longer tasks in ChatGPT

For a multi-step change, ask for the whole outcome in one request:

```text
Use CodeLocal in my selected project. Inspect the relevant code, make the change,
run the relevant checks, and report the files changed and test results. Continue
from any unfinished CodeLocal checkpoint while this chat turn is still active.
```

An `agent` result with `status=needs_continuation` means that one bounded group of actions ended while the task still needs work. The AI client should inspect `nextAction` and continue with another call. `status=ready` means CodeLocal's verification gate passed. The AI client controls how long its own turn runs, so CodeLocal cannot force ChatGPT to keep working after the host ends a turn. If that happens, ask it to continue the same task; the current checkout and available task context remain accessible. Approvals still require your action.

## 7. Approvals

Read-only actions are generally cheaper and safer than mutations.

Risky or side-effecting actions can require approval, for example:

- Git commit or push;
- destructive filesystem operations;
- dangerous terminal commands;
- actions outside normal safe automation policy.

CodeLocal never treats a learned workflow as permission to bypass the current security policy.

## 8. Update

Stable:

```bash
npm i -g codelocal@latest
```

Beta:

```bash
npm i -g codelocal@beta
```

Then:

```bash
codelocal --version
codelocal
```

Normal package updates keep existing pairing and authorized workspaces.

## 9. Reset or uninstall

Reset CodeLocal local state while keeping the CLI installed:

```bash
codelocal reset --all
```

Remove local state and the global package:

```bash
codelocal uninstall --all
```

Operating-system permissions such as Accessibility or Screen Recording remain controlled by the OS.

## 10. Useful commands

```text
codelocal --version
codelocal status
codelocal workspaces
codelocal grant <path>
codelocal ungrant <path-or-id>
codelocal doctor <path>
codelocal stop
codelocal reset --all
codelocal uninstall --all
codelocal approvals list
codelocal mcp list
```

## 11. If an AI client sees an old tool schema

CodeLocal keeps a compact public MCP surface. When the server/tool schema changes, an already-open MCP connection can sometimes still hold an older action definition.

Refresh or reconnect CodeLocal in that AI client so it scans the latest tools/actions. This does not remove your local device pairing, Project Brain or workspace grants.
