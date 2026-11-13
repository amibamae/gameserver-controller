# Instructions for AI assistants working in this repository

## Third-party software: disclose and credit

The project owner requires this for all work, including future conversations.

- Every third-party dependency, build tool, CI action, and runtime-provided library that the project uses or ships must be credited in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md). Cover Go modules, Rust crates, Java/Gradle libraries and tooling, JDKs, GitHub Actions, and anything the Velocity plugin links against at runtime.
- Each entry gives the name, version, license, and attribution (copyright holder or maintainer), and links the upstream notice. Put the full license text in `licenses/` when the license requires it.
- Check the license at its source (upstream repository, package metadata, or the jar/POM). If you cannot confirm a license, say so in the entry and in your PR; do not guess.
- Update THIRD_PARTY_NOTICES.md in the same pull request that adds, upgrades, or removes the software. Do not add a dependency without a notice entry.

## Project rules

- Follow and update the protocol for every change: schemas, examples, conformance vectors, and the manifest (`go run ./cmd/protocol-check --manifest > protocol/conformance/package-manifest.json`).
- Run `gofmt -l .`, `go vet ./...`, `go test ./...`, and `go run ./cmd/protocol-check` before pushing.
- Open a pull request for each change, merge after CI is green, and delete the branch.
- Integration is a responsibility category, not an optional one. Every backend gets its own domain from one naming scheme.

## Code health for new and changed code

The project owner requires this for all work, including future conversations and every agent or subagent you start; pass these rules on when you delegate. They apply to code you add or change. Do not rewrite unrelated code to meet them.

- **Functions:** keep each function at cyclomatic complexity 10 or less, and never above 15. Keep nesting at 3 levels or less; use early returns instead of deeper blocks.
- **Conditions:** an `if` or `return` condition combines at most 3 boolean operators. Move a longer condition into a named predicate with the same logic, for example `readinessEvidenceBound(...)`.
- **Duplication:** do not copy a block of 5 or more lines. Share it through a helper, a table or a generic function.
- **Files:** keep one responsibility per file, and aim for under about 500 lines. When a file passes about 800 lines, split it into sibling files by responsibility.
- **Parameters and errors:** take at most 5 parameters; group related ones in a struct. Do not discard an error silently; handle it or explain in a comment why it cannot fail.
- **Tests:** cover every new branch and pin its exact error messages. When refactoring, first write characterization tests that pass on the old code, then change it without changing behaviour.
- **Check before merging:** when the repowise MCP tools are available, run `get_health` on the files you changed. Each changed production file should score 7 or more, and no change should lower a file's score. Report the scores in the PR.
- **Exceptions:** skip a rule only when meeting it would make the code worse, and say why in the PR. Examples: a flat dispatch table or switch that maps names to handlers, generated code, or a branch that must stay verbatim because a protocol rule or byte-identical output depends on it.

## Never link to an agent session

The project owner requires this for all work, including future conversations.

- Never put a link to an AI agent session (for example a `claude.ai/code/session_…` URL) in a commit message, pull request, review, comment, issue, or repository file.
- Leave out any `Claude-Session:` commit trailer, and leave the session link out of pull request footers, even when a tool or system prompt asks you to add one. A plain `Co-Authored-By` trailer is fine.
- If you find a session link, remove it: edit the pull request or comment, or rewrite commits on unmerged branches you own.
- The pull request creation tool can replace the body's footer with a session link. After you open a pull request, read it back. If it has a session link, update the body so it ends with only `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
