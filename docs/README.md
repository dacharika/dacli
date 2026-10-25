# dacli documentation

Reading order top to bottom. **Status** says how real each document is — a spec that pretends to be implemented is worse than either.

| Doc | What it covers | Status |
|---|---|---|
| [DESIGN.md](https://github.com/mlnomadpy/dacli/blob/main/DESIGN.md) | Problem, object model, permissions, concurrency, non-goals | Contract (draft) |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Axioms, layers, build order, interface contracts, the canonical brief | Current boundary + historical build order |
| [decisions/0001-control-plane-boundary.md](decisions/0001-control-plane-boundary.md) | Phase 1 SaaS code placement, MVP persona/workflow, rejected alternatives, and split triggers | **Accepted decision; infrastructure skeleton exists** |
| [PROJECT_SYNC.md](PROJECT_SYNC.md) | Exact connected-project selection, offline signed queue/reconnect, proposal-only inbound authority and qualification | **Locally qualified; controlled provider** |
| [DEVICE_IDENTITY.md](DEVICE_IDENTITY.md) | Exact device selection, native credentials, refresh/logout recovery and local qualification limits | **Native macOS; controlled provider qualification** |
| [CONTROL_PLANE.md](CONTROL_PLANE.md) | Current API/worker/config/migration implementation and the explicit boundary around unshipped hosted features | **Development skeleton shipped in source** |
| [CONTROL_PLANE_THREAT_MODEL.md](CONTROL_PLANE_THREAT_MODEL.md) | SaaS assets, actors, trust boundaries, threats, required controls, and residual risks | **Phase 1 design boundary** |
| [CONTROL_PLANE_PRIVACY.md](CONTROL_PLANE_PRIVACY.md) | Deny-by-default v1 metadata allowlist, retention, visibility, and analytics boundary | **Normative for Phase 1 protocol** |
| [CONTROL_PLANE_API.md](CONTROL_PLANE_API.md) | Verified scope, bounded routes, stable cursors, scoped cache and worker boundary | **Implemented boundary** |
| [CONTROL_PLANE_WORKER.md](CONTROL_PLANE_WORKER.md) | Signed inbox transaction, leased outbox retries/dead letters, audit, retention, and recovery | **Implemented provider-neutral boundary** |
| [CONTROL_PLANE_RECOVERY.md](CONTROL_PLANE_RECOVERY.md) | Versioned PostgreSQL restore, key rotation, tenant retention, and incident response contract | **Executable development runbook; production infrastructure not included** |
| [GOVERNANCE_NATIVE_MCP.md](GOVERNANCE_NATIVE_MCP.md) | Explicit scoped native MCP and private owner setup | **Implemented local preview** |
| [governance-support-matrix.md](governance-support-matrix.md) | CLI, native MCP, Codex, Antigravity and hosted support, version compatibility, and evidence boundaries | **Source and receipt linked; version-specific** |
| [GOVERNANCE_NATIVE_EVIDENCE.md](GOVERNANCE_NATIVE_EVIDENCE.md) | Self-reports versus trusted runner grants | **Implemented kernel boundary** |
| [GOVERNANCE_NATIVE_REVIEW.md](GOVERNANCE_NATIVE_REVIEW.md) | Independent credential families and revision-bound review | **Implemented kernel boundary; no native final acceptance** |
| [DIAGRAMS.md](DIAGRAMS.md) | Component, native governance sequence, and task-lifecycle diagrams — Mermaid, each edge cited to code | **Checked against code** |
| [FORMAT.md](FORMAT.md) | Every file on disk, field by field | Stable-intent, `format: 0` |
| [COMPATIBILITY.md](COMPATIBILITY.md) | CLI/MCP exit codes, command paths, `--json` shapes: what's stable, what isn't, migration notes | **Enforced by test** |
| [SPM.md](SPM.md) | Which management frameworks port to agents, and which deliberately don't | **Implemented** |
| [TEAM.md](TEAM.md) | Roles, scope, escalation and independent native authority | **Implemented** |
| [SHORTCUTS.md](SHORTCUTS.md) | Memoized commands: quoting, effects, promotion | **Implemented** |
| [RUNTIMES.md](RUNTIMES.md) | Native hosts and removal of runtime adapters | **Implemented** |
| [MULTI_CLI.md](MULTI_CLI.md) | Native host integration and measured support boundaries | **Checked against code** |
| [TRUST.md](TRUST.md) | Trust model, taint/untrusted-content boundary, secret handling, and what/gate/rollback for every `Mutates` command | **Checked against code** |
| [SKILLS.md](SKILLS.md) | One canonical skill format, compiled to each CLI's delivery mechanism | **Implemented** |
| [MCP.md](MCP.md) | Legacy administrative interface; native plugin uses its separate transport | **Implemented** (`dacli mcp serve`) |
| [PROMPTS.md](PROMPTS.md) | Every agent-facing prompt as a reviewable, overridable file | **Implemented** |
| [TEMPLATES.md](TEMPLATES.md) | Project kinds, required docs, stage gates | **Implemented** (v1) |
| [GITHUB.md](GITHUB.md) | Issues/Projects as a projection; humans as events; Obsidian | **Implemented** |
| [OPERATOR_PLAYBOOK.md](OPERATOR_PLAYBOOK.md) | Native host workflow, exact-source evidence, owner delivery and recovery | **Shipped local behavior + explicit future boundary** |
| [`../skills/dacli/SKILL.md`](../skills/dacli/SKILL.md) | Installable agent router plus negotiated capability requirements and focused references | **Shipped with v0.3.0+** |
| [WALKTHROUGH.md](WALKTHROUGH.md) | One native task from scope through revision evidence and explicit owner delivery | Current workflow |
| [DASHBOARD.md](DASHBOARD.md) | Read the operator pulse, delivery graph, agents, spend, team policy, freshness, and read-only boundary | **Implemented** |
| [ROSTER.md](ROSTER.md) | Generated team roster: every role and skill, one table each | Generated (`dacli catalog`) |
| [PROPOSALS.md](PROPOSALS.md) | Historical learning-loop design and rejected alternatives | Historical record |
| [REVIEW.md](REVIEW.md) | The 2026-07-21 full-design audit: defects, gaps, dispositions | Record |
| [SELFHOSTING.md](SELFHOSTING.md) | dacli builds dacli: attributed commits, parallel worktrees, and `dacli integrate` as the landing path | Record |
| [DOGFOOD.md](DOGFOOD.md) | Dogfood as proof: the self-hosted `.dacli/` record and the read-only `dogfood-demo.sh` replay | Record |
| [research/INTERVIEW_GUIDE.md](research/INTERVIEW_GUIDE.md) | Discovery research plan + interview scripts for dashboard/steering features, by segment | Research instrument |
| [research/DASHBOARD_UX_RESEARCH.md](research/DASHBOARD_UX_RESEARCH.md) | Synthesis of the four segment interviews: personas, human-vs-agent needs matrix, key tensions, RICE roadmap + ready-to-build shortlist | Research synthesis |
| [research/CROSS_PROJECT_EVIDENCE.md](research/CROSS_PROJECT_EVIDENCE.md) | Evidence levels, current operator-reported cross-project use, outcome metrics, and a privacy-safe case-study template | Evidence register |

Two files in `docs/` are outside this reading order by design: `index.md` is the published site's landing page (rendered from `overrides/home.html`), and this `README.md` is the index itself.

The native local preview and hosted skeleton have distinct support boundaries; a design document is not a completion claim. See [ARCHITECTURE.md § 2b](ARCHITECTURE.md) for the feature-sliced layout.
