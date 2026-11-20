# Instructions for AI coding agents

This file is for agents that read `AGENTS.md`, such as ChatGPT Codex. Claude sessions read the same rules from `CLAUDE.md`.

## Working style (owner requirements)

For all development tasks in this repository:

- Work on a dedicated feature branch, never directly on main.
- Run appropriate tests before committing.
- Create small, descriptive local Git commits after completing and validating each task.
- Never commit secrets, credentials, or local progress files (`.opencode/` is ignored; keep it that way).
- Do not push to GitHub, create pull requests, or merge changes without the owner's explicit approval.
- Update `.opencode/WORK_PROGRESS.md` after each task.
- Report the branch name, commit hash, test results, and next steps.

Follow these rules unless the owner explicitly instructs otherwise.

1. Read [CLAUDE.md](CLAUDE.md). Its rules are standing requirements from the project owner and apply to every agent, and to every subagent you start (pass them on when you delegate):
   - credit all third-party software in `THIRD_PARTY_NOTICES.md` in the same PR that adds, upgrades, or removes it;
   - work protocol first;
   - open one PR per change and merge only after green CI;
   - follow the code-health limits for new and changed code;
   - never link to an agent session.
2. Read [docs/AGENT_HANDOFF.md](docs/AGENT_HANDOFF.md) for the current work, branches in progress, and what to do next.
3. Before pushing, run the pre-push checks:
   - `gofmt -l .` (it must print nothing; run `gofmt -w .` if it does),
   - `go vet ./...`,
   - `go test ./...`,
   - `go run ./cmd/protocol-check`,
   - `go run ./cmd/complexity-check -base origin/main` (must report nothing for your changed functions; see the exception rule in CLAUDE.md),
   - `go run ./cmd/config-check validate configs/controller.example.yaml`.
4. If the repowise MCP tools are available, run `get_health` on the files you changed and report the scores in the PR; each changed production file should score 7 or more and no change may lower a file's score.
5. Pull request checklist: what you changed and why, which components are affected (including compatibility changes and side effects), which tests you added or ran (or why testing does not apply), which docs and examples changed, and the purpose and license of any new dependency. Follow the rules in [CONTRIBUTING.md](CONTRIBUTING.md): security-sensitive changes get extra detail, tests must not touch production systems or data, and credentials, private keys, tokens, and private data never go into commits, issues, PRs, logs, or test fixtures.
6. Update the protocol manifest when schemas, examples, or conformance vectors change: `go run ./cmd/protocol-check --manifest > protocol/conformance/package-manifest.json`.

## Architecture

- Go controller for game-server lifecycles; the reference stack is Vultr, Wasabi, Paper, Velocity on Debian. Go is the implementation language, not the wire protocol (JSON Schema 2020-12, JSON-RPC 2.0, OpenAPI 3.2.1).
- The core owns lifecycle rules, permissions, approvals, audit, and production/staging boundaries. It never calls vendor APIs directly; adapters report capabilities and outcomes and make no safety decisions for the core.
- Keep the five adapter categories distinct: hosting, storage, execution, game, integration (proxy/DNS). Keep game behavior out of execution, and provider branches out of core lifecycle code; new providers and games must fit through an adapter.
- Durable rules: persist the operation record before any mutating provider call; reconcile the actual resource before retrying after an unclear provider answer; an upload acknowledgment is not backup proof; a failed or uncertain backup blocks destruction.
- Staging cannot write to production storage or sync a test world back; it restores one selected timestamped production backup under read-only credentials.
- Start from [README.md](README.md), [docs/ROADMAP.md](docs/ROADMAP.md), and [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); protocol contracts live in [protocol/](protocol/).

## Testing

- Cover every new branch and pin exact error messages; before refactoring, add characterization tests that pass on the old code, then change behavior-preservingly (same error messages, check order, persisted formats, database write order, transaction boundaries).
- Tests must not point at production systems, production data, or real credentials; isolate any infrastructure they need, and use the scripted fakes in `internal/fakeadapter` for adapter work.
- Run at least the CONTRIBUTING.md pre-PR checks locally: `go test -count=1 ./...` and `go test -race -count=1 ./...` (plus `go mod verify`, `go build ./...`, and the `cmd/` checks above).

## Security

- Follow [SECURITY.md](SECURITY.md) and [docs/SECURITY_MODEL.md](docs/SECURITY_MODEL.md). Telemetry is off by default and requires explicit opt-in, separate per player when it identifies one.
- Never write credentials, keys, tokens, or private data into code, config, logs, fixtures, or commits; read secrets only through the bounded credential accessor and secret references. Prefer least privilege and safe failure, and guard destructive actions against accidental data loss.
- Changes touching auth, storage, lifecycle, SSH/firewall, backups, releases, telemetry, or plugin execution need extra detail in the PR: threat model, permissions, failure behavior, and tests.
- Report exploitable vulnerabilities privately (GitHub private reporting), never in a public issue before maintainers can act.

## Git safety

- One branch and one PR per change; never push, merge, force-push, or delete branches you do not own. Only the maintainers merge into the project, and a green CI run does not guarantee acceptance.
- Commit messages and PR bodies describe the change only: no agent session links (see CLAUDE.md) and no secrets, per CONTRIBUTING.md.

## Git Branch Isolation and Subagent Delegation

**Effective immediately:** All agents must use dedicated feature branches for work. Subagents never push, merge, or create branches - only the parent agent coordinates these operations. This prevents accidental changes to main, respects the repository's Git safety rules, and ensures all code is reviewed through PRs with green CI checks.

### Branch Policy
- Work on a named feature branch from `main`
- One focused task per branch
- Commit frequently with descriptive messages
- Never force-push or delete branches you don't own
- Merge only when parent agent approves (auto-merge applies when CI passes + code health ≥ 7)

### Subagent Delegation
- **Parent agent** (this session): orchestrates Git operations, merges PRs, manages branch lifecycle
- **Subagents**: perform exploratory work, code changes, local testing - NEVER push or merge
- Before delegating: clarify task scope, expected outputs, and approval process
- After delegation: monitor progress via `<subagent>` messages

### Pre-Merge Checklist (Parent Agent)
1. All required CI checks pass (green ✅)
2. Code health score ≥ 7 for changed files
3. Review PR diff for correctness and edge cases
4. Update AGENTS.md or docs as needed
5. Create one PR per change group targeting main

### Auto-Merge Policy
When CI passes and code health is acceptable:
- **Merge**: squash merge into main automatically
- **No owner approval needed** when all checks pass
- **Exception**: breaking changes, security fixes, or scope expansions require manual review first

### Blocked PRs Handling
PRs with failing tests (CI red), conflicts with main, or code health < 7 are:
1. Not merged until issues resolved
2. Documented in `.opencode/WORK_PROGRESS.md`
3. Closed only after changes integrated elsewhere OR if no longer needed

---

## Protocol Conformance Files

Located in `cmd/protocol-check/` for static validation of schemas, examples, and conformance vectors:

- `backup_capture_acceptance_test.go`: backup capture protocol acceptance
- `bootstrap_template_conformance.go`: bootstrap template structure conformance
- `conformance_operations.go`: operation conformance registry management
- `execution_health_conformance.go`: execution unit health conformance
- `execution_unit_inspect_conformance.go`: execution unit inspection conformance
- `game_compatibility_conformance.go`: game compatibility protocol conformance
- `game_restore_evidence_conformance.go`: game restore evidence conformance
- `hosting_instance_create_recovery.go`: hosting instance creation recovery conformance

Use `go run ./cmd/protocol-check --manifest` to regenerate the package manifest when schemas/examples change.

---

## Context management

- Keep one focused task per session and update [docs/AGENT_HANDOFF.md](docs/AGENT_HANDOFF.md) when work stops mid-stream, so the next agent can resume.
- Keep the local, untracked `.opencode/WORK_PROGRESS.md` current (completed work, current task, blockers, next steps) and never commit it.
