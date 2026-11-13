# Instructions for AI coding agents

This file is for agents that read `AGENTS.md`, such as ChatGPT Codex. Claude sessions read the same rules from `CLAUDE.md`.

1. Read [CLAUDE.md](CLAUDE.md). Its rules are standing requirements from the project owner and apply to every agent:
   - credit all third-party software;
   - work protocol first;
   - open one PR per change and merge only after green CI;
   - follow the code-health limits for new and changed code;
   - never link to an agent session.
2. Read [docs/AGENT_HANDOFF.md](docs/AGENT_HANDOFF.md) for the current work, branches in progress, and what to do next.
3. Before pushing, run `gofmt -l .` (it must print nothing), `go vet ./...`, `go test ./...` and `go run ./cmd/protocol-check`.
