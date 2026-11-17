# Agent Orchestration Guide

## ⚠️ Environment Status: Single-Session Mode

**This repository runs in a Qwen3.5:9b/Ollama environment without native subagent support.** All work must be done sequentially within a single session.

See `.opencode/WORKING_MODE.md` for the current session's capabilities. The procedures below describe how to work in this constrained environment, plus guidance for when you switch to a platform that supports subagents (like Claude Code).

---

## 1. Current Working Mode: Sequential Refinement

Since native subagent spawning is unavailable here, all tasks must be completed **sequentially**:

### Workflow for Single-Session Environments

1. **Read coordination files** first:
   - `.opencode/AGENT_ORCHESTRATION.md` (this guide)
   - `.opencode/WORK_PROGRESS.md` (current task and blockers)
   - `docs/AGENT_HANDOFF.md` (session handoff notes)

2. **Complete one file fully** before starting the next:
   - Read the source file to understand its structure
   - Plan the refactoring changes
   - Make atomic edits
   - Verify with build and tests
   - Commit locally

3. **Move to the next task** only after verification passes

4. **Batch related tests**: After completing a group of related files, run all tests together to catch any interactions

5. **Create focused commits**: Each logical change gets its own commit message for clear history

### Benefits of Sequential Approach Here

- Simpler verification (no race conditions)
- Clearer Git history (no interleaved changes)
- Easier debugging when issues arise
- No session state management needed

---

## 2. Future Sessions with Subagent Support

If you ever switch to a platform that supports native subagents (like Claude Code), follow this procedure:

### When to Spawn Subagents

Spawn subagents for **independent tasks** that:
- Don't share state or dependencies
- Can be verified independently
- Don't require sequential ordering

**Example independent tasks:**
- Refactor `server_destroy.go` (extract handlers)
- Refactor `server_stop.go` (extract handlers)
- Update documentation for a specific component

**Example dependent tasks (don't parallelize):**
- Build + test workflow A → then workflow B
- Any changes that affect shared state

### How to Create New Child Sessions (When Available)

For a **new child session**:
- Provide `agent`: e.g., `"general"` or `"explore"`
- Provide `description`: short task description (3-5 words)
- Provide `prompt`: complete task instructions
- **DO NOT supply `sessionID`** when creating a new session

Example:
```javascript
tools.subagent({
  agent: 'general',
  description: 'Refactor server_destroy.go',
  prompt: 'Analyze internal/workflow/server_destroy.go and extract lifecycle state handlers.'
})
```

### How to Continue Existing Child Sessions

Pass the `sessionID` (starting with "ses") to continue that child:
```javascript
tools.subagent({
  agent: 'general',
  description: 'Update WORK_PROGRESS.md',
  prompt: 'Continue updating .opencode/WORK_PROGRESS.md...',
  sessionID: 'sesXXXX'  // Use exact value from spawn response
})
```

### How to Verify Successful Agent Creation

1. Receive a `<subagent>` message when the child completes or is idle
2. The returned `sessionID` starts with "ses" prefix
3. Check `.opencode/WORK_PROGRESS.md` for the spawned agent's task
4. **Monitor for `<subagent>` messages** - don't assume success

### How to Delegate Independent Work

1. Identify independent tasks that can run in parallel
2. Assign distinct file ownership (e.g., one agent handles `server_destroy.go`, another handles `server_stop.go`)
3. Provide complete context in the prompt including:
   - File paths to analyze/modify
   - Specific refactoring goals
   - Verification steps (build, test)
   - Reporting requirements (update WORK_PROGRESS.md)

### How the Primary Build Agent Coordinates Children

The primary agent must:
- Read `.opencode/AGENT_ORCHESTRATION.md` before spawning
- Check existing assignments in `.opencode/WORK_PROGRESS.md` and `docs/AGENT_HANDOFF.md`
- Avoid overlapping file ownership between children
- Record assigned session IDs and their purposes
- Collect actual child session IDs (don't fabricate)
- Review all changes before integration
- Run tests and inspect final Git diff

### How to Collect and Review Child Results

1. Monitor `<subagent>` messages for progress/completion
2. Wait for completion indicators (`completed` status)
3. Collect outputs via file edits (automatically visible)
4. Review changes by reading modified files
5. Run verification tests before merge approval

### How to Handle Failed or Unavailable Agents

1. If a child doesn't respond after 10-15 minutes, check if it's stuck
2. Look for error messages in the conversation context
3. If truly hung:
   - Kill via `gstack-runaway-stopper`
   - Re-spawn with adjusted instructions
4. Document failure in `.opencode/WORK_PROGRESS.md`

### How to Avoid Fabricated Session IDs

**NEVER invent session IDs.** Always use the exact value returned from the spawn call. Common mistakes:
- Making up "sesXXXX" patterns
- Reusing old session IDs
- Claiming parallel work without actual spawns

### How to Maintain the Repository Coordination File

Update `.opencode/WORK_PROGRESS.md` after each delegation:
- Add new spawned agents' session IDs and tasks
- Record completion status when children finish
- Note blockers or waiting states
- Remove completed entries when moving on

---

## 3. Git Safety Requirements (Always Apply)

Regardless of subagent availability:

- Work on a dedicated feature branch (never main)
- Run tests before committing: `go test -count=1 ./...`
- Create small, descriptive commits per task
- Never commit secrets or progress files to git (`.opencode/` is ignored)
- Do not push without explicit owner approval
- Update `.opencode/WORK_PROGRESS.md` after each task

---

## 4. Workflow Summary

**With subagents available:**
1. Read AGENTS.md and .opencode/AGENT_ORCHESTRATION.md
2. Check docs/AGENT_HANDOFF.md for current work state
3. Identify independent tasks suitable for parallelization
4. Spawn subagents with disjoint responsibilities
5. Monitor `<subagent>` messages for progress
6. Wait for all children to complete
7. Review all changes and run verification tests
8. Create PR when ready (await owner approval)
9. Update .opencode/WORK_PROGRESS.md and docs/AGENT_HANDOFF.md

**Without subagents (current environment):**
1. Read AGENTS.md and .opencode/AGENT_ORCHESTRATION.md
2. Check docs/AGENT_HANDOFF.md and .opencode/WORK_PROGRESS.md for current state
3. Identify the next task in priority order from REFACTORING_SUMMARY.md
4. Complete the task fully (refactor, verify, commit)
5. Move to the next task
6. Continue until all refactoring is complete

---

## 5. Working Mode Detection

At session start, determine if subagents are available:

```bash
# Check tool catalog for subagent capability
# If tools.subagent doesn't appear in available tools, use sequential mode
```

Update `.opencode/WORKING_MODE.md` to reflect current capabilities.

---

## 6. Recommended Refactoring Order

Based on the workflow files analyzed, refactor in this order:

1. **server_stop.go** - Simplest lifecycle (2 main states)
2. **server_destroy.go** - More complex with 4 inspection steps
3. **server_restore.go** - Medium complexity
4. **server_start.go** - Start lifecycle

See `REFACTORING_SUMMARY.md` and `docs/AGENT_HANDOFF.md` for detailed analysis of each file.
