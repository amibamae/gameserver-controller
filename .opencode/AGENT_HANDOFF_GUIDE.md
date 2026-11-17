# OpenCode Subagent Usage Guide

This document explains how to create and use parallel subagents in OpenCode for complex refactoring tasks.

## How to Create Parallel Subagents

To spawn new parallel subagents using OpenCode's native subagent spawning capability:

### Basic Syntax
```
/subagent (agent=<agent_id>, description=<short_task>, prompt=<complete_instructions>)
```

### Important Rules
- **DO NOT** supply `sessionID` when creating a new child - let the system assign one automatically
- **DO NOT** invent session IDs
- **DO NOT** use continuation operations to create agents
- Agent types available: `explore`, `general`, `qa`, etc. (see available skills)

### Example Spawns
```
/subagent (agent=explore, description=bolt.go complexity analysis, prompt=read internal/store/bolt.go and identify remaining complex expressions...)
/subagent (agent=general, description=record.go simplification, prompt=read internal/store/record.go and focus on validSecretReference function...)
```

## Spawned Subagents List

### Active Session IDs
- `ses_e185c43d1ffess3lGWRq1tPDCI` (explore) — bolt.go complexity analysis - COMPLETED
- `ses_e185c0b37ffedoXp8IVWroXH31` (general) — record.go simplification - COMPLETED  
- `ses_e185bfab0ffe0cQxr1O7dpbZ2p` (general) — capacity.go refactoring - COMPLETED

## Review Process
Regularly check subagent progress using the `<subagent>` notification messages. When complete, review their findings and implement recommended changes.

## Current Work State

See `.opencode/WORK_PROGRESS.md` for the current status of all tasks.
