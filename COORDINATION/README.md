# Coordination System

Multi-agent coordination system for managing concurrent development in the repository.

## Features

- Git-backed optimistic concurrency control
- JSON-based registries (lightweight, no heavy databases)
- File ownership tracking and conflict detection
- Task assignment with dependency awareness
- Audit logging for all operations
- Subagent branch isolation support

## Directory Structure

```
COORDINATION/
├── registry.json       # Task registry with IDs, phases, acceptance criteria
├── .agents.json        # Agent registrations and capabilities
├── .deps.json          # Dependency graph between tasks
├── .files.json         # File ownership tracking
├── audit.md            # Append-only operation log
└── README.md           # This file
```

## Usage

### Claiming a Task

When an agent claims a task, update `registry.json`:

```json
{
  "assignedTo": "agent-2",
  "claimedAt": "2026-11-25T10:30:00Z"
}
```

### File Reservation

Mark shared files as reserved in `.files.json` to prevent conflicts:

```json
{
  "shared-config-updates": ["configs/controller.example.yaml"]
}
```

### Dependencies

Tasks declare dependencies on other tasks. The coordinator can warn agents about unresolved dependencies before they begin work.

## Subagent Integration

The coordination system works with the subagent isolation policy defined in `AGENTS.md`:

- Each subagent runs in its own Git worktree
- Branch names follow convention: `opencode/<agent-type>/<task-description>`
- Agents never share worktrees or branches

## Verification

Before pushing changes:

1. Run verification checks on coordination CLI tool (if available)
2. Format check with `gofmt -l COORDINATION/`
3. Verify no conflicts with existing implementation files
