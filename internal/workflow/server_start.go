package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	startInspectInstanceStep = "inspect-instance"
	startRuntimeStep         = "start-runtime"
	startVerifyRuntimeStep   = "verify-runtime"
	startReadinessStep       = "check-readiness"
)

// ServerStartProfile contains trusted release-specific values that do not
// belong in a client request. A resolver selects it from the stored logical
// server definition and controller-owned catalogs.
type ServerStartProfile struct {
	GameReleaseID       string
	UnitRef             json.RawMessage
	ReadinessCheckSetID string
	RequiredChecks      []ReadinessRequirement
}

// ServerStartProfileResolver must resolve immutable, trusted deployment
// metadata. It must not derive adapter steps from client-supplied fields.
type ServerStartProfileResolver interface {
	ResolveServerStart(context.Context, store.LogicalServer) (ServerStartProfile, error)
}

type serverStartParameters struct {
	ExpectedInstanceRef json.RawMessage `json:"expected_instance_ref,omitempty"`
}

type instanceInspection struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
	State       string          `json:"state"`
	ObservedAt  string          `json:"observed_at"`
}

type executionUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

type executionUnitStartResult struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	Outcome    string          `json:"outcome"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

// ServerStartWorkflow is both the trusted planner and the durable definition
// for server.start. It uses the same injected object during acceptance and
// execution so profile selection and lifecycle policy share one source.
type ServerStartWorkflow struct {
	servers  store.LifecycleOperationStore
	adapters adapterRuntime
	schemas  schemaRuntime
	profiles ServerStartProfileResolver
	ttl      time.Duration
}

func NewServerStartWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime, profiles ServerStartProfileResolver, assertionTTL time.Duration) (*ServerStartWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, and trusted start-profile resolver are required")
	}
	if assertionTTL <= 0 || assertionTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	return &ServerStartWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles, ttl: assertionTTL}, nil
}

var _ interface {
	BuildPlan(context.Context, store.OperationRecord) ([]store.OperationStep, error)
	Definition
	LifecycleProjector
	interface {
		ValidateCompletion(context.Context, store.OperationRecord) error
	}
} = (*ServerStartWorkflow)(nil)

// planSchemas returns the schemas when w holds every dependency planning
// needs, and nil otherwise, which checkPlanRequest refuses.
func (w *ServerStartWorkflow) planSchemas() schemaRuntime {
	if w == nil || w.profiles == nil || !lifecycleRuntimeReady(w.servers, w.adapters, w.schemas) {
		return nil
	}
	return w.schemas
}

var startPlanRule = runtimePlanRule{
	operationType:  "server.start",
	initError:      "initialized start workflow and context are required",
	operationError: "server.start operation and loaded protocol version are required",
	label:          "start plan",
	state:          protocolstate.ServerStopped,
	stateError:     "server.start requires a stopped logical server with a managed instance",
}

// BuildPlan resolves the server's current instance and trusted release profile
// into a durable, schema-checked sequence. All resource references, check IDs,
// versions, and adapter capability versions are stored before dispatch.
func (w *ServerStartWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if err := checkPlanRequest(ctx, w.planSchemas(), operation, startPlanRule); err != nil {
		return nil, err
	}
	var parameters serverStartParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return nil, errors.New("decode server.start parameters")
	}
	server, err := loadPlannedServer(w.servers, operation, parameters.ExpectedInstanceRef, startPlanRule)
	if err != nil {
		return nil, err
	}
	profile, err := resolveTrustedProfile(ctx, server, w.profiles.ResolveServerStart, validateStartProfile, "start")
	if err != nil {
		return nil, err
	}
	rows := startPlanRows(operation, server, profile)
	capabilities, err := resolvePlannedCapabilities(w.adapters, w.schemas.ProtocolVersion(), rows)
	if err != nil {
		return nil, err
	}
	if err := w.verifyGameReadiness(server, profile); err != nil {
		return nil, err
	}
	return encodePlannedSteps(w.schemas, rows, capabilities)
}

// startPlanRows is the server.start plan: inspect the provider instance,
// start the pinned unit, inspect it, then check game readiness.
func startPlanRows(operation store.OperationRecord, server store.LogicalServer, profile ServerStartProfile) []plannedRuntimeStep {
	return []plannedRuntimeStep{
		{id: startInspectInstanceStep, adapterID: adapterIDFromRef(server.InstanceRef), category: adapter.CategoryHosting,
			capability: "hosting.instance.inspect", schemaStem: "hosting:instance-inspect",
			input: map[string]any{"instance_ref": server.InstanceRef}},
		{id: startRuntimeStep, adapterID: server.ExecutionAdapterID, category: adapter.CategoryExecution,
			capability: "execution.unit.start", schemaStem: "execution:systemd-unit-start", mutating: true,
			input: map[string]any{"unit_ref": profile.UnitRef}},
		{id: startVerifyRuntimeStep, adapterID: server.ExecutionAdapterID, category: adapter.CategoryExecution,
			capability: "execution.unit.inspect", schemaStem: "execution:systemd-unit-inspect",
			input: map[string]any{"unit_ref": profile.UnitRef}},
		{id: startReadinessStep, adapterID: server.GameAdapterID, category: adapter.CategoryGame,
			capability: "game.readiness.check", schemaStem: "game:readiness-check",
			input: map[string]any{
				"server_id": operation.Target.ServerID, "environment_id": operation.Target.EnvironmentID,
				"instance_ref": server.InstanceRef, "game_release_id": profile.GameReleaseID,
				"readiness_check_set_id": profile.ReadinessCheckSetID, "required_checks": profile.RequiredChecks,
			}},
	}
}

// verifyGameReadiness requires the configured game adapter to declare the
// profile's exact readiness check set for the server's environment.
func (w *ServerStartWorkflow) verifyGameReadiness(server store.LogicalServer, profile ServerStartProfile) error {
	gameDescriptor, err := w.adapters.Descriptor(server.GameAdapterID)
	if err != nil {
		return fmt.Errorf("resolve game adapter %q: %w", server.GameAdapterID, err)
	}
	if gameDescriptor.Category != adapter.CategoryGame {
		return errors.New("configured game adapter has the wrong category")
	}
	return verifyReadinessSetDeclared(gameDescriptor, server.EnvironmentID, profile)
}

func validateStartProfile(server store.LogicalServer, profile ServerStartProfile) error {
	if !startProfileSelectsBoundedChecks(profile) {
		return errors.New("trusted start profile must select a release and bounded readiness check set")
	}
	if ref, bound := decodeBoundUnitRef(profile.UnitRef, server); !bound || ref.ResourceKind == "" {
		return errors.New("trusted start profile unit reference does not bind the configured execution adapter and environment")
	}
	return validateReadinessChecks(profile.RequiredChecks)
}

// startProfileSelectsBoundedChecks reports whether the profile names a
// bounded release and check set with between 1 and 64 required checks.
func startProfileSelectsBoundedChecks(profile ServerStartProfile) bool {
	return boundedProfileID(profile.GameReleaseID, 200) && boundedProfileID(profile.ReadinessCheckSetID, 200) &&
		len(profile.RequiredChecks) > 0 && len(profile.RequiredChecks) <= 64
}

func validateReadinessChecks(checks []ReadinessRequirement) error {
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if check.CheckID == "" || check.CheckVersion == "" {
			return errors.New("trusted readiness checks require non-empty IDs and versions")
		}
		if _, duplicate := seen[check.CheckID]; duplicate {
			return fmt.Errorf("trusted readiness profile repeats check %q", check.CheckID)
		}
		seen[check.CheckID] = struct{}{}
	}
	return nil
}

func verifyReadinessSetDeclared(descriptor adapter.Descriptor, environmentID string, profile ServerStartProfile) error {
	gameCapability, declared := readinessCapability(descriptor)
	if !declared {
		return errors.New("game adapter does not declare game.readiness.check")
	}
	// The declarations stay an anonymous struct: its type name is part of the
	// pinned decode error message.
	encoded, err := json.Marshal(gameCapability.Constraints)
	if err != nil {
		return err
	}
	var constraints struct {
		ReadinessCheckSets []struct {
			GameReleaseID       string                 `json:"game_release_id"`
			EnvironmentIDs      []string               `json:"environment_ids"`
			ReadinessCheckSetID string                 `json:"readiness_check_set_id"`
			RequiredChecks      []ReadinessRequirement `json:"required_checks"`
		} `json:"readiness_check_sets"`
	}
	if err := json.Unmarshal(encoded, &constraints); err != nil {
		return fmt.Errorf("decode game adapter readiness declarations: %w", err)
	}
	for _, declared := range constraints.ReadinessCheckSets {
		if declared.GameReleaseID != profile.GameReleaseID || declared.ReadinessCheckSetID != profile.ReadinessCheckSetID ||
			!containsString(declared.EnvironmentIDs, environmentID) || !sameReadinessChecks(declared.RequiredChecks, profile.RequiredChecks) {
			continue
		}
		return nil
	}
	return errors.New("trusted readiness profile does not match an exact game-adapter release, environment, set, and check-version declaration")
}

// readinessCapability returns the descriptor's first game.readiness.check
// declaration, if any.
func readinessCapability(descriptor adapter.Descriptor) (adapter.Capability, bool) {
	for _, capability := range descriptor.Capabilities {
		if capability.Name == "game.readiness.check" {
			return capability, true
		}
	}
	return adapter.Capability{}, false
}

func sameReadinessChecks(left, right []ReadinessRequirement) bool {
	if len(left) != len(right) {
		return false
	}
	versions := make(map[string]string, len(left))
	for _, check := range left {
		if check.CheckID == "" || check.CheckVersion == "" {
			return false
		}
		if _, duplicate := versions[check.CheckID]; duplicate {
			return false
		}
		versions[check.CheckID] = check.CheckVersion
	}
	for _, check := range right {
		version, exists := versions[check.CheckID]
		if !exists || version != check.CheckVersion {
			return false
		}
		delete(versions, check.CheckID)
	}
	return len(versions) == 0
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// startStepCheck rechecks one planned server.start step against the stored
// server and the evidence of the steps before it.
type startStepCheck func(store.OperationRecord, store.OperationStep, store.LogicalServer) error

var startStepChecks = map[string]startStepCheck{
	startInspectInstanceStep: checkStartInspectInstance,
	startRuntimeStep:         checkStartRuntime,
	startVerifyRuntimeStep:   checkStartVerifyRuntime,
	startReadinessStep:       checkStartReadiness,
}

// CheckPreconditions rechecks server identity and all prior-step evidence just
// before each planned dispatch.
func (w *ServerStartWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.start" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.start operation and context are required")
	}
	server, err := loadPreconditionServer(ctx, w.servers, operation)
	if err != nil {
		return err
	}
	check, supported := startStepChecks[step.StepID]
	if !supported {
		return fmt.Errorf("unsupported server.start step %q", step.StepID)
	}
	return check(operation, step, server)
}

func checkStartInspectInstance(_ store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	if !stepBindsRef(step.Input, "instance_ref", server.InstanceRef) {
		return errors.New("provider inspection is not bound to the current managed instance")
	}
	if server.LifecycleState != protocolstate.ServerStopped {
		return errors.New("server must remain stopped until the provider instance is confirmed active")
	}
	return nil
}

func checkStartRuntime(operation store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	if !startRuntimeAttemptAllowed(step.Attempt, server.LifecycleState) || !stepSucceeded(operation, startInspectInstanceStep) {
		return errors.New("runtime start requires a stopped server or confirmed start retry and a successful provider inspection")
	}
	if !instanceObservedActive(operation, startInspectInstanceStep, server.InstanceRef) {
		return errors.New("runtime start requires the exact provider instance to be active")
	}
	if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") {
		return errors.New("runtime start and verification must target the same planned unit")
	}
	return nil
}

// startRuntimeAttemptAllowed allows the first start attempt from a stopped
// server and a retry only after the server was confirmed starting.
func startRuntimeAttemptAllowed(attempt int, state protocolstate.ServerState) bool {
	initialAttempt := attempt == 0 && state == protocolstate.ServerStopped
	confirmedRetry := attempt > 0 && state == protocolstate.ServerStarting
	return initialAttempt || confirmedRetry
}

func checkStartVerifyRuntime(operation store.OperationRecord, _ store.OperationStep, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerStarting || !unitStartCompleted(operation) {
		return errors.New("runtime inspection requires a completed start command")
	}
	if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") {
		return errors.New("runtime verification must target the exact started unit")
	}
	return nil
}

func checkStartReadiness(operation store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerStarting || !stepSucceeded(operation, startVerifyRuntimeStep) {
		return errors.New("game readiness requires a successful exact-unit runtime inspection")
	}
	inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	if err != nil || inspection.State != "running" {
		return errors.New("game readiness requires the execution unit to be confirmed running")
	}
	if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") || !stepBindsRef(step.Input, "instance_ref", server.InstanceRef) {
		return errors.New("readiness request must be bound to the verified unit and current instance")
	}
	return nil
}

// HandlePreconditionFailure terminally fails only when a stored, target-bound
// inspection proves the next side effect cannot proceed. Unknown observations
// remain suspended so the controller never guesses about provider state.
func (w *ServerStartWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	message := startPreconditionFailureMessage(operation, step.StepID)
	if message == "" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	return preconditionUnmet(operation.OperationID, message), nil
}

// startPreconditionFailureMessage explains a proven-unmet precondition, or
// returns "" when the stored evidence does not prove one.
func startPreconditionFailureMessage(operation store.OperationRecord, stepID string) string {
	switch stepID {
	case startRuntimeStep:
		if startInstanceProvenInactive(operation) {
			return "The exact provider instance is not active; no runtime start was issued."
		}
	case startReadinessStep:
		if startUnitProvenNotRunning(operation) {
			return "The exact execution unit is not confirmed running; game readiness was not checked."
		}
	}
	return ""
}

func startInstanceProvenInactive(operation store.OperationRecord) bool {
	observation, observed := plannedInstanceObservation(operation, startInspectInstanceStep)
	return observed && instanceDefinitivelyInactive(observation.State)
}

func startUnitProvenNotRunning(operation store.OperationRecord) bool {
	observation, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	if err != nil || !equalJSON(observation.UnitRef, stepField(operationStep(operation, startRuntimeStep).Input, "unit_ref")) {
		return false
	}
	switch observation.State {
	case "absent", "stopped", "starting", "stopping", "failed":
		return true
	}
	return false
}

// Reconcile resolves an ambiguous unit start by using the exact read-only
// inspection capability pinned in the same durable plan. It never retries
// until inspection confirms the unit is absent or stopped.
func (w *ServerStartWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil || step.StepID != startRuntimeStep || step.CapabilityName != "execution.unit.start" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	needsOperator := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	inspect := operationStep(operation, startVerifyRuntimeStep)
	if !pinnedUnitInspect(inspect, step) {
		return needsOperator, nil
	}
	observation, observed := observeUnit(ctx, w.adapters, w.schemas, unitProbe{
		operation: operation, step: step, inspect: inspect, unitRef: stepUnitRef(step),
		requestID: startReconcileRequestID(operation.OperationID, step.StepID, step.Attempt),
	})
	if !observed {
		return needsOperator, nil
	}
	return startUnitResolution(operation, step, observation), nil
}

// startUnitResolution maps the observed state of the exact unit to the
// resolution of its ambiguous start.
func startUnitResolution(operation store.OperationRecord, step store.OperationStep, observation executionUnitInspection) Resolution {
	switch observation.State {
	case "running":
		return Resolution{State: ResolutionCompleted, Output: startedUnitOutput(observation), ExternalOperationRef: step.ExternalOperationRef}
	case "absent", "stopped":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}
	case "starting":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}
	case "failed":
		return Resolution{
			State: ResolutionDefinitiveError, ExternalOperationRef: step.ExternalOperationRef,
			Error: protocolError("core.execution.unit_failed", "provider_failure", "The exact execution unit is confirmed failed after a start attempt.", operation.OperationID, false, "operator_review", true),
		}
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	}
}

func startedUnitOutput(observation executionUnitInspection) json.RawMessage {
	// The struct holds strings and a unit reference decoded from valid JSON,
	// so encoding it cannot fail.
	output, _ := json.Marshal(executionUnitStartResult{
		UnitRef: append(json.RawMessage(nil), observation.UnitRef...), Outcome: "completed",
		State: "running", ObservedAt: observation.ObservedAt,
	})
	return output
}

func stepUnitRef(step store.OperationStep) json.RawMessage {
	return stepField(step.Input, "unit_ref")
}

func startReconcileRequestID(operationID, stepID string, attempt int) string {
	return reconcileRequestID(operationID, stepID, "reconcile", attempt)
}

// ValidateCompletion enforces the runtime and readiness postconditions after
// all adapter steps have completed. A failed game check is a terminal failed
// start operation, not a falsely successful workflow.
func (w *ServerStartWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.start" {
		return errors.New("server.start completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !unitStartCompleted(operation) {
		return errors.New("execution unit start postcondition was not confirmed")
	}
	if !startUnitConfirmedRunning(operation) {
		return errors.New("execution unit is not confirmed running at the pinned reference")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	_, err = BuildReadinessAssertion(w.schemas, operation, stored.Record, startReadinessStep, w.ttl)
	return err
}

// unitStartCompleted reports whether the succeeded start step reported its
// own planned unit completed and running.
func unitStartCompleted(operation store.OperationRecord) bool {
	for _, step := range operation.Steps {
		if step.StepID != startRuntimeStep || step.Status != protocolstate.StepSucceeded {
			continue
		}
		var result executionUnitStartResult
		return json.Unmarshal(step.Output, &result) == nil && result.Outcome == "completed" && result.State == "running" && equalJSON(result.UnitRef, stepUnitRef(step))
	}
	return false
}

// startUnitConfirmedRunning reports whether the runtime inspection observed
// the started unit running.
func startUnitConfirmedRunning(operation store.OperationRecord) bool {
	inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	return err == nil && inspection.State == "running" && equalJSON(inspection.UnitRef, stepUnitRef(operationStep(operation, startRuntimeStep)))
}

func (w *ServerStartWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	assertion, err := BuildReadinessAssertion(w.schemas, operation, stored.Record, startReadinessStep, w.ttl)
	if err != nil {
		return nil, err
	}
	result := lifecycleResult{
		operationType: "server.start", summary: "Server runtime started and all required game readiness checks passed.",
		affected: []any{stored.Record.InstanceRef},
	}.document(operation)
	result["operation_data"] = map[string]any{
		"server_id": stored.Record.ServerID, "environment_id": stored.Record.EnvironmentID,
		"instance_ref": stored.Record.InstanceRef, "lifecycle_state": string(protocolstate.ServerReady),
		"readiness_assertion": assertion,
	}
	return json.Marshal(result)
}

func (w *ServerStartWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	result := lifecycleResult{
		operationType: "server.start", summary: "Server start did not satisfy all runtime and readiness postconditions.",
		failed:     failedStepSummaries(step, failure),
		unresolved: startUnresolvedConditions(operation, step),
		partial:    operationStep(operation, startRuntimeStep).Attempt > 0,
	}
	if instanceRef := w.failedStartInstance(operation); len(instanceRef) > 0 {
		result.affected = []any{instanceRef}
	}
	return json.Marshal(result.document(operation))
}

// startUnresolvedConditions explains an operation that failed without a
// failed step: a proven-unmet precondition when the stored error says so,
// otherwise unmet postconditions.
func startUnresolvedConditions(operation store.OperationRecord, step store.OperationStep) []any {
	if step.StepID != "" {
		return []any{}
	}
	conditionID := "server.start.postcondition_unmet"
	description := "The adapter calls completed, but the exact running and readiness postconditions were not satisfied."
	var protocolFailure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(operation.Error, &protocolFailure) == nil && protocolFailure.Code == "core.safety.precondition_unmet" && protocolFailure.Message != "" {
		conditionID = "server.start.precondition_unmet"
		description = protocolFailure.Message
	}
	return []any{map[string]any{"condition_id": conditionID, "description": description}}
}

// failedStartInstance names the instance a failed start affected: the
// targeted one, else the server's current one.
func (w *ServerStartWorkflow) failedStartInstance(operation store.OperationRecord) json.RawMessage {
	if len(operation.Target.InstanceRef) > 0 {
		return operation.Target.InstanceRef
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		// A failure result must still be written when the server cannot be
		// loaded; it then lists no affected resource.
		return nil
	}
	return server.Record.InstanceRef
}

// ProjectLifecycle derives start's durable states only from immutable steps.
// The ready transition occurs atomically with the terminal success result.
func (w *ServerStartWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if !projectionTargets(operation, "server.start", current.Record) {
		return store.LogicalServer{}, false, errors.New("server.start lifecycle projection target mismatch")
	}
	next := current.Record
	switch current.Record.LifecycleState {
	case protocolstate.ServerStopped:
		return projectStartFromStopped(next, operation)
	case protocolstate.ServerStarting:
		return projectStartFromStarting(next, operation)
	case protocolstate.ServerValidating:
		return w.projectStartFromValidating(next, operation)
	}
	return current.Record, false, nil
}

// projectStartFromStopped moves a stopped server to starting once the start
// step has left pending.
func projectStartFromStopped(next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, bool, error) {
	if operationStep(operation, startRuntimeStep).Status == protocolstate.StepPending {
		return next, false, nil
	}
	next.LifecycleState = protocolstate.ServerStarting
	next.DesiredState = "running"
	return next, true, nil
}

// projectStartFromStarting fails a starting server when the operation failed
// or the unit was observed failed, and moves it to validating once the
// readiness step has left pending.
func projectStartFromStarting(next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationFailed {
		next.LifecycleState = protocolstate.ServerFailed
		return next, true, nil
	}
	unitFailed, err := startVerificationObservedFailure(operation)
	if err != nil {
		return store.LogicalServer{}, false, err
	}
	if unitFailed {
		next.LifecycleState = protocolstate.ServerFailed
		return next, true, nil
	}
	if operationStep(operation, startReadinessStep).Status == protocolstate.StepPending {
		return next, false, nil
	}
	next.LifecycleState = protocolstate.ServerValidating
	next.DesiredState = "running"
	return next, true, nil
}

// startVerificationObservedFailure reports whether a succeeded runtime
// inspection observed the unit failed.
func startVerificationObservedFailure(operation store.OperationRecord) (bool, error) {
	verifyStep := operationStep(operation, startVerifyRuntimeStep)
	if verifyStep.Status != protocolstate.StepSucceeded {
		return false, nil
	}
	var observed executionUnitInspection
	if json.Unmarshal(verifyStep.Output, &observed) != nil {
		return false, errors.New("execution unit verification output cannot be decoded")
	}
	return observed.State == "failed", nil
}

// projectStartFromValidating makes a validating server ready only with an
// exact terminal success result, and degrades it when the operation failed.
func (w *ServerStartWorkflow) projectStartFromValidating(next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, bool, error) {
	switch operation.Status {
	case protocolstate.OperationSucceeded:
		if !hasValidStartResult(operation.Result, operation.Target, next.InstanceRef, w.schemas) {
			return store.LogicalServer{}, false, errors.New("terminal server.start result lacks its exact readiness assertion")
		}
		next.LifecycleState = protocolstate.ServerReady
		next.DesiredState = "running"
		return next, true, nil
	case protocolstate.OperationFailed:
		next.LifecycleState = protocolstate.ServerDegraded
		return next, true, nil
	}
	return next, false, nil
}

func hasValidStartResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	var result lifecycleResultDocument
	if json.Unmarshal(raw, &result) != nil || !resultNamesTarget(result, "server.start", protocolstate.ServerReady, target) ||
		!isObject(result.OperationData.Assertion) {
		return false
	}
	if !resultBindsInstance(result.OperationData.InstanceRef, target.InstanceRef, instanceRef) {
		return false
	}
	return schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:readiness-assertion", result.OperationData.Assertion) == nil
}
