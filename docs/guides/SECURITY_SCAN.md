# CodeLocal Security Scan

`codelocal security` runs supported security scanners against the selected local project, normalizes their findings, writes local reports, and optionally asks the CodeLocal cloud AI service to triage the sanitized findings.

## Security model

The local report is the source of truth. CodeLocal does not upload the project tree as part of this command.

Before AI review, findings are normalized and sanitized:

- Gitleaks secret values and matches are never deserialized into the normalized finding.
- Raw scanner evidence is excluded from the AI payload.
- Project, branch, commit, file paths, package names, versions, scanner messages, fixes, and metadata stay local.
- The AI payload contains only validated scanner identifiers, severity, line number, and CVE/CWE identifiers.
- The AI endpoint uses the existing CodeLocal device credential and signed-request verification.
- Report directories use private permissions and remain under the gitignored `.codelocal/` tree.

Use `--offline` when no cloud request is allowed.

## Usage

```bash
codelocal security
codelocal security --offline
codelocal security --root /path/to/project
codelocal security --engines gitleaks,trivy
codelocal security --fix
codelocal security --no-setup
codelocal security setup
codelocal security setup --engines gitleaks,trivy
codelocal security report
codelocal security report --open
```

Supported engines in V1:

- Gitleaks: credential and secret detection.
- Trivy: dependency vulnerabilities and configuration / IaC findings.
- Semgrep: source-code static analysis.

The command can run with any subset of available engines. For Gitleaks and Trivy, an online scan automatically prepares CodeLocal-managed binaries when they are missing. The managed binaries use pinned versions and SHA-256 checksums, live under `~/.codelocal/security/tools`, and are never resolved through an unpinned "latest" download. Run `codelocal security setup` to prepare them explicitly, or use `--no-setup` to require already-installed tools.

V1 intentionally keeps Semgrep externally managed because its Python wheel has a wider dependency chain that must be pinned as a whole before CodeLocal can safely auto-install it. Install Semgrep separately or point `CODELOCAL_SECURITY_SEMGREP_PATH` at a trusted executable. `CODELOCAL_SECURITY_GITLEAKS_PATH` and `CODELOCAL_SECURITY_TRIVY_PATH` can similarly override managed binaries.

In offline mode, CodeLocal never performs scanner setup or AI calls. Trivy uses offline scanning and does not update its databases or checks. Semgrep requires `CODELOCAL_SEMGREP_CONFIG` to resolve to a readable local file or directory instead of fetching a remote ruleset.

## Reports

Each scan writes:

```text
.codelocal/security/reports/
├── latest.json
├── latest.md
├── latest.sarif
├── baselines/
│   ├── gitleaks.json
│   ├── trivy.json
│   └── semgrep.json
└── history/
    └── <scan-id>.json
```

A partial report is still written when no scanner is available or an engine fails. This makes scanner readiness and failures auditable rather than losing the scan attempt.

The JSON report includes the scan ID, Git branch and commit, engine statuses, severity summary, normalized findings, AI state, and comparison against the previous scan. Findings are grouped as new, fixed, or remaining by stable fingerprint. Successful scans update a private baseline for that engine. Failed or unavailable engines keep their previous baseline, so their findings become unassessed instead of incorrectly fixed. History remains the migration fallback for reports written before per-engine baselines existed. Markdown renders the comparison, and SARIF marks current findings with baseline state plus AI properties.

`codelocal security report` prints `latest.md`. Add `--open` to open that file with the system application.

`codelocal security --fix` additionally writes `fix-plan.json` and `fix-plan.md`. In V1 this is deliberately advisory: it never runs shell commands and never edits project source. Secret findings prioritize credential rotation/revocation; dependency findings use scanner fixed-version metadata when available; other findings prefer validated AI or scanner remediation text and still require manual review.

## AI review

Without `--offline`, CodeLocal sends only the sanitized `AIPayload` to:

```text
POST /api/client/security/analyze
```

The endpoint:

- requires valid device authentication and the existing signed-request proof;
- rate-limits analysis requests;
- caps request size, provider response size, output tokens, and AI review to the first 50 normalized findings;
- sends scan/finding IDs, fingerprints, engine, rule/category, severity, start line, and CVE/CWE only; project, Git metadata, file paths, package data, scanner messages, fixes, evidence, and raw source stay local;
- filters model output to finding IDs that were present in the request;
- clamps confidence values and normalizes triage states;
- returns defensive triage and remediation metadata;
- records only scan-level audit metadata, not raw source or secrets.

The cloud administrator configures the Security-only model:

```env
CODELOCAL_SECURITY_LLM_API_KEY=...
CODELOCAL_SECURITY_LLM_BASE_URL=https://api.openai.com/v1
CODELOCAL_SECURITY_LLM_MODEL=gpt-4o-mini
```

The base URL must use HTTPS. These server-side settings do not fall back to dashboard models, `OPENAI_API_KEY`, or user-provided model credentials. The API key is never returned to the CLI. Security AI review requires a device credential with signed-request proof. Authenticated users are limited to 5 AI reviews per minute and 50 per day; rate-limit storage failure blocks model calls.

AI output is merged back into the local report. If AI review is unavailable, the local scanner report remains usable.

## V1 boundary

V1 provides scan orchestration, normalization, comparison/baselines, reports, AI triage, checksum-pinned managed Gitleaks/Trivy setup, and an advisory `security --fix` remediation plan. V1 does **not** automatically modify project source or run remediation commands. Semgrep managed installation remains deferred until its full Python dependency graph can be pinned and verified.
