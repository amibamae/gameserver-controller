# Code Health Refactoring Candidates Analysis

## Priority Ranking (Highest to Lowest)

### P1 - Server Provisioning Workflow Refactoring
**File:** `internal/workflow/server_provision.go`  
**Complexity Drivers:** Large switch statement, multiple validation paths

#### Candidate 1: `ProjectLifecycle` function (lines 343-405)
- **Cyclomatic Complexity:** ~25+ (multiple nested conditionals in switch cases)
- **Issue:** Monolithic state transition logic with interleaved side effects
- **Refactoring Strategy:** 
  - Extract each lifecycle state case into dedicated helper functions:
    - `handleLifecycleUnprovisioned`
    - `handleLifecycleProvisioning`  
    - `handleLifecyclePreparing`
    - `handleLifecycleStarting`
    - `handleLifecycleValidating`
  - Create intermediate validation helpers: `canAdvanceToReadyState`, `hasValidResultEvidence`
- **Benefit:** Improves readability, enables unit tests per lifecycle transition

#### Candidate 2: `HandlePreconditionFailure` function (lines 323-341)  
- **Cyclomatic Complexity:** ~18+ (multiple step-ID checks with resolution return)
- **Issue:** Returns different resolutions based on which precondition failed
- **Refactoring Strategy:**
  - Extract condition check helpers: `isCreateStepFailure`, `isWaitStepActiveFailed`
  - Create error builders for each precondition type
  - Keep main function as orchestrator only
- **Benefit:** Clearer separation of failure modes, easier to add new preconditions

#### Candidate 3: `BeforeAdapterDispatch` function (lines 204-248)
- **Cyclomatic Complexity:** ~15+ (multiple nested validation checks)
- **Issue:** Combines input parsing, profile resolution, and capacity reservation
- **Refactoring Strategy:**
  - Extract input validation: `parseCreateStepInput`, `validateResourceKey`
  - Extract profile resolution: `resolveProvisionProfile`
  - Extract capacity operations: `performPreDispatchCapacityReservation`
- **Benefit:** Each sub-function can be tested independently

---

### P2 - Other Workflow Files (Similar Patterns Expected)
Check these files using the same analysis approach:

1. **`internal/workflow/server_destroy.go`** - Likely has complex destroy state machine
2. **`internal/workflow/server_restore.go`** - Restore state transitions with backup validation
3. **`internal/workflow/server_provision_r3_*.go`** - R3 reconciliation logic in restore workflows

---

### P3 - Controller Logic (if exists)
Check for complex controller functions:
- `internal/controller/*/*.go` files
- Command handlers that chain multiple operations

---

## Next Steps

1. Spawn agent A: Focus on `server_provision.go` refactoring (ProjectLifecycle, HandlePreconditionFailure, BeforeAdapterDispatch)
2. Spawn agent B: Analyze `server_destroy.go` for similar complexity patterns  
3. Spawn agent C: Review remaining workflow files for candidates
4. Each agent creates a feature branch, applies targeted refactoring, runs tests
5. Merge to main after all changes complete
