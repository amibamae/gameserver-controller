# Code Refactoring Plan - Priority Candidates

## Analysis Complete ✓

Based on manual code analysis of `internal/workflow/server_provision.go` and surrounding files, I've identified high-priority refactoring candidates.

---

## P1: server_provision.go Functions (Ready to Refactor)

### 1.1 `ProjectLifecycle` function (lines 343-405)
**Complexity Score:** 8/10  
**Issue:** Large switch statement handling lifecycle state transitions with validation logic mixed in

**Refactoring Strategy:**
```go
// Split into these helpers:
func handleLifecycleUnprovisioned(w *ServerProvisionWorkflow, next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, error) { ... }
func handleLifecycleProvisioning(w *ServerProvisionWorkflow, next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, error) { ... }
func handleLifecyclePreparing(w *ServerProvisionWorkflow, next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, error) { ... }
func handleLifecycleStarting(w *ServerProvisionWorkflow, next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, error) { ... }
func handleLifecycleValidating(w *ServerProvisionWorkflow, next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, error) { ... }

// Main function becomes orchestrator:
func (w *ServerProvisionWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
    if mismatch { return store.LogicalServer{}, false, errors.New(...) }
    
    switch current.Record.LifecycleState {
    case protocolstate.ServerUnprovisioned: return handleLifecycleUnprovisioned(w, next, operation)
    case protocolstate.ServerProvisioning: return handleLifecycleProvisioning(w, next, operation)
    // etc...
    }
}
```

**Benefits:**
- Each lifecycle state can have dedicated unit tests
- Clearer contract boundaries between states  
- Easier to understand error conditions per state

### 1.2 `HandlePreconditionFailure` function (lines 323-341)  
**Complexity Score:** 7/10  
**Issue:** Returns different resolutions based on which precondition failed; mixed logic

**Refactoring Strategy:**
```go
func isCreateStepFailure(step store.OperationStep, operation store.OperationRecord) bool { ... }
func isWaitStepActiveFailed(operation store.OperationRecord) bool { ... }
func buildResolutionForCreateFailure(operationID, stepID string) Resolution { ... }
func buildResolutionForWaitFailure(operationID, stepID string) Resolution { ... }
func buildResolutionForBootstrapFailure(operationID, stepID string) Resolution { ... }

func (w *ServerProvisionWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
    if isCreateStepFailure(step, operation) { return buildResolutionForCreateFailure(...) }
    if isWaitStepActiveFailed(operation) && stepSucceeded(operation, provisionCreateStep) { return buildResolutionForWaitFailure(...) }
    // etc...
}
```

**Benefits:**
- Each precondition check is testable in isolation
- Clear separation of failure modes
- Easier to add new preconditions without breaking existing logic

### 1.3 `BeforeAdapterDispatch` function (lines 204-248)  
**Complexity Score:** 6/10  
**Issue:** Combines input parsing, profile resolution, validation, and capacity reservation

**Refactoring Strategy:**
```go
func parseCreateStepInput(input []byte) (map[string]any, error) { ... }
func validateResourceKey(key string, operationID string) error { ... }
func resolveProvisionProfile(ctx context.Context, server store.LogicalServer, params json.RawMessage) (ServerProvisionProfile, error) { ... }
func performPreDispatchCapacityReservation(reservation store.CapacityReservation, limit, groupLimit int) error { ... }

func (w *ServerProvisionWorkflow) BeforeAdapterDispatch(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
    if ctx.Err() != nil { return ctx.Err() }
    if step.StepID != provisionCreateStep { return w.observeCreatedCapacity(operation) }
    
    input, err := parseCreateStepInput(step.Input)
    if err != nil { return errors.New("persisted provider create step has invalid trusted input") }
    
    stored, _ := w.servers.GetLogicalServer(...)
    profile, err := resolveProvisionProfile(ctx, stored.Record, operation.RequestSnapshot.Parameters)
    if err != nil { return err }
    
    if validateProvisionProfile(stored.Record, request, profile) != nil { return err }
    
    // etc...
}
```

**Benefits:**
- Each validation step is testable independently
- Clear error propagation paths
- Easier to mock dependencies for unit tests

---

## P2: Other Workflow Files (Needs Analysis)

After completing P1 refactoring, analyze these similar files using same strategy:

1. **`internal/workflow/server_destroy.go`** - Likely has complex destroy state machine
2. **`internal/workflow/server_restore.go`** - Restore operations with backup validation  
3. **`internal/workflow/server_provision_r3_*.go`** - R3 reconciliation logic in restore workflows

---

## Workflow for Multi-Agent Execution

Since direct repowise tool access is not available, here's the manual workflow:

### Step 1: Create Feature Branches
```bash
git checkout main
git pull origin main

# Branch for server_provision refactoring
git checkout -b feat/refactor-server-provision-workflow

# Track progress in .opencode/WORK_PROGRESS.md
```

### Step 2: Refactor P1 Candidates
- Apply changes to `ProjectLifecycle`, `HandlePreconditionFailure`, `BeforeAdapterDispatch`
- Run tests: `go test -count=1 ./internal/workflow/server_provision_test.go`
- Fix any regressions

### Step 3: Analyze and Refactor Other Workflow Files
- Move from main to worktree for parallel analysis
- Apply same extraction pattern to similar functions in other workflow files

### Step 4: Health Check & Cleanup
```bash
go build ./...
go test -count=1 ./internal/workflow/...
git status
```

### Step 5: Update Documentation
- Update `.opencode/WORK_PROGRESS.md` with completion status
- Document helper functions extracted and their purposes
- Note any breaking changes (should be minimal with proper extraction)

---

## Success Criteria

After completing refactoring:
1. ✅ All modified files compile (`go build ./...`)
2. ✅ Tests pass for core functionality (ignoring pre-existing environment issues)
3. ✅ Code health scores 7+ on repowise when available
4. ✅ Cyclomatic complexity reduced per function
5. ✅ Each extracted helper has clear, single responsibility

---

## Risk Assessment

### Low Risk ✓
- Extracting helpers from functions preserves semantics
- Helper functions are pure (no side effects)  
- Original inline calls can be compared against helper invocations

### Medium Considerations
- Need to verify error messages match when refactoring
- Some functions may share state between handlers - ensure no cross-contamination
- Test coverage must increase as we extract new functions

---

## Next Actions (Priority Order)

1. **Immediate:** Apply refactoring to `server_provision.go` P1 candidates
2. **After P1 Complete:** Analyze `server_destroy.go` and `server_restore.go`  
3. **Parallel Work:** Create separate worktree/branch for each major file being refactored
4. **Final:** Run full test suite, update documentation, refresh health scores
