package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerProvisionWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	server, request, err := w.provisionPreconditionState(ctx, operation)
	if err != nil {
		return err
	}
	switch step.StepID {
	case provisionCompatibilityStep:
		if request.RestoreBackup == "" {
			return errors.New("backup compatibility step is not part of this provision request")
		}
		return nil
	case provisionCreateStep:
		return w.checkProvisionCreate(ctx, operation, server, request, step)
	}
	instanceRef, err := provisionInstanceRef(operation, server)
	if err != nil {
		return err
	}
	if err := checkProvisionStepBinding(operation, step, instanceRef); err != nil {
		return err
	}
	rule, ok := provisionPrerequisites[step.StepID]
	if !ok {
		return fmt.Errorf("unsupported server.provision step %q", step.StepID)
	}
	return rule(provisionPrerequisiteCheck{ctx: ctx, workflow: w, operation: operation, server: server, restoring: request.RestoreBackup != ""})
}

// provisionPreconditionState checks the context and operation, then loads
// the target server and decodes the persisted request.
func (w *ServerProvisionWorkflow) provisionPreconditionState(ctx context.Context, operation store.OperationRecord) (store.LogicalServer, provisionRequest, error) {
	if ctx == nil || operation.OperationType != "server.provision" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return store.LogicalServer{}, provisionRequest{}, errors.New("server.provision operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return store.LogicalServer{}, provisionRequest{}, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.LogicalServer{}, provisionRequest{}, err
	}
	server := stored.Record
	if server.ServerID != operation.Target.ServerID || server.EnvironmentID != operation.Target.EnvironmentID {
		return store.LogicalServer{}, provisionRequest{}, store.ErrLogicalServerTargetMismatch
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return store.LogicalServer{}, provisionRequest{}, errors.New("persisted provision request cannot be decoded")
	}
	return server, request, nil
}

// checkProvisionCreate allows the provider create only once, for a server
// with no instance yet, and after a compatible backup when restoring.
func (w *ServerProvisionWorkflow) checkProvisionCreate(ctx context.Context, operation store.OperationRecord, server store.LogicalServer, request provisionRequest, step store.OperationStep) error {
	if request.RestoreBackup != "" {
		profile, err := w.profiles.ResolveServerProvision(ctx, server, operation.RequestSnapshot.Parameters)
		if err != nil || !provisionCompatibilitySucceeded(operation, w.schemas, profile) {
			return errors.New("provider create requires a complete compatible-backup result")
		}
	}
	initial := server.LifecycleState == protocolstate.ServerUnprovisioned || server.LifecycleState == protocolstate.ServerProvisioning
	if !initial || len(server.InstanceRef) != 0 || step.Attempt != 0 {
		return errors.New("provider create requires an unprovisioned logical server")
	}
	return nil
}

// provisionInstanceRef is the server's instance, or the one the provider
// create step verifiably returned.
func provisionInstanceRef(operation store.OperationRecord, server store.LogicalServer) (json.RawMessage, error) {
	if len(server.InstanceRef) != 0 {
		return server.InstanceRef, nil
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil {
		return nil, errors.New("provision step requires a verified created instance")
	}
	return created.InstanceRef, nil
}

// checkProvisionStepBinding requires a unit step to use the created unit on
// the created instance, and any other step to name the created instance.
func checkProvisionStepBinding(operation store.OperationRecord, step store.OperationStep, instanceRef json.RawMessage) error {
	if step.StepID == provisionStartUnitStep || step.StepID == provisionInspectUnitStep {
		createdUnit := operationStep(operation, provisionCreateUnitStep)
		if !equalJSON(stepField(step.Input, "unit_ref"), stepField(createdUnit.Input, "unit_ref")) ||
			!provisionTargetBinds(createdUnit, instanceRef) {
			return fmt.Errorf("provision step %q is not bound to the exact created unit and instance", step.StepID)
		}
		return nil
	}
	if !provisionStepBinds(step, "instance_ref", instanceRef) && !provisionTargetBinds(step, instanceRef) {
		return fmt.Errorf("provision step %q is not bound to the exact created instance", step.StepID)
	}
	return nil
}

// provisionPrerequisiteCheck is what a step's prerequisite rule reads.
type provisionPrerequisiteCheck struct {
	ctx       context.Context
	workflow  *ServerProvisionWorkflow
	operation store.OperationRecord
	server    store.LogicalServer
	restoring bool
}

func (c provisionPrerequisiteCheck) succeeded(stepID string) bool {
	return stepSucceeded(c.operation, stepID)
}

// requirePrerequisite returns message as an error unless ok.
func requirePrerequisite(ok bool, message string) error {
	if ok {
		return nil
	}
	return errors.New(message)
}

// provisionPrerequisites maps each step after the provider create to the
// earlier results it needs before it may be dispatched.
var provisionPrerequisites = map[string]func(provisionPrerequisiteCheck) error{
	provisionWaitStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionCreateStep), "instance wait requires a completed provider create")
	},
	provisionBootstrapStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionWaitStep) && provisionWaitSucceeded(c.operation), "bootstrap requires the exact instance to be confirmed active")
	},
	provisionBootstrapInspect: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionBootstrapStep), "bootstrap inspection requires a completed delivery request")
	},
	provisionInstallStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionBootstrapInspect) && provisionBootstrapSucceeded(c.operation), "game installation requires successful inspection of the pinned bootstrap")
	},
	provisionRestoreStep: func(c provisionPrerequisiteCheck) error {
		profile, err := c.workflow.profiles.ResolveServerProvision(c.ctx, c.server, c.operation.RequestSnapshot.Parameters)
		ok := err == nil && c.succeeded(provisionInstallStep) && provisionInstallSucceeded(c.operation) &&
			provisionCompatibilitySucceeded(c.operation, c.workflow.schemas, profile)
		return requirePrerequisite(ok, "backup restore requires verified installation and a compatible exact backup")
	},
	provisionRestoreInspectStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionRestoreStep), "restore inspection requires the recorded restore call")
	},
	provisionCreateUnitStep: func(c provisionPrerequisiteCheck) error {
		if !c.succeeded(provisionInstallStep) || !provisionInstallSucceeded(c.operation) {
			return errors.New("execution unit creation requires verified installation of the pinned game release")
		}
		return requirePrerequisite(!c.restoring || provisionRestoreSucceeded(c.operation), "execution unit creation requires verified completion of the requested backup restore")
	},
	provisionStartUnitStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionCreateUnitStep) && provisionUnitCreated(c.operation), "execution unit start requires a verified stopped or unchanged unit")
	},
	provisionInspectUnitStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionStartUnitStep) && provisionUnitStarted(c.operation), "unit inspection requires a completed start")
	},
	provisionReadinessStep: func(c provisionPrerequisiteCheck) error {
		ok := c.succeeded(provisionInspectUnitStep) && provisionUnitRunning(c.operation) && (!c.restoring || provisionRestoreSucceeded(c.operation))
		return requirePrerequisite(ok, "game readiness requires the exact execution unit to be running")
	},
}

func provisionStepBinds(step store.OperationStep, path string, expected json.RawMessage) bool {
	input := step.Input
	if len(step.ResolvedInput) > 0 {
		input = step.ResolvedInput
	}
	return stepBindsRef(input, path, expected)
}

func provisionTargetBinds(step store.OperationStep, expected json.RawMessage) bool {
	input := step.Input
	if len(step.ResolvedInput) > 0 {
		input = step.ResolvedInput
	}
	return stepBindsRef(stepField(input, "target"), "instance_ref", expected)
}

func provisionCreateStepOutput(operation store.OperationRecord) (provisionCreateOutput, error) {
	var output provisionCreateOutput
	step := operationStep(operation, provisionCreateStep)
	if step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil || !isObject(output.InstanceRef) {
		return provisionCreateOutput{}, errors.New("provider create output missing")
	}
	var request provisionRequest
	ref, valid := decodeProvisionRef(output.InstanceRef)
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil || !valid ||
		!provisionRefIs(ref, request.HostingAdapter, operation.Target.EnvironmentID, "instance") {
		return provisionCreateOutput{}, errors.New("provider create output is not bound to the expected adapter, environment, and instance kind")
	}
	return output, nil
}

func provisionWaitSucceeded(operation store.OperationRecord) bool {
	var out provisionWaitOutput
	step := operationStep(operation, provisionWaitStep)
	created, err := provisionCreateStepOutput(operation)
	if err != nil || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &out) != nil {
		return false
	}
	return out.OperationStatus == "succeeded" && out.InstanceState == "active" && equalJSON(out.InstanceRef, created.InstanceRef)
}
func provisionBootstrapSucceeded(operation store.OperationRecord) bool {
	var out provisionBootstrapInspection
	step := operationStep(operation, provisionBootstrapInspect)
	return step.Status == protocolstate.StepSucceeded && json.Unmarshal(step.Output, &out) == nil && out.State == "succeeded" && provisionBootstrapMatches(operation, out)
}
func provisionBootstrapMatches(operation store.OperationRecord, out provisionBootstrapInspection) bool {
	var expected struct {
		Bootstrap struct {
			Revision string `json:"revision"`
			Digest   string `json:"digest"`
		} `json:"bootstrap"`
	}
	step := operationStep(operation, provisionBootstrapStep)
	if json.Unmarshal(step.Input, &expected) != nil || out.Revision != expected.Bootstrap.Revision || out.Digest != expected.Bootstrap.Digest {
		return false
	}
	return equalJSON(out.InstanceRef, stepField(step.ResolvedInput, "instance_ref")) &&
		equalJSON(out.BootstrapExecutionRef, stepField(step.Output, "bootstrap_execution_ref"))
}
func provisionInstallSucceeded(operation store.OperationRecord) bool {
	var out struct {
		Target struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID       string                  `json:"release_id"`
		Outcome         string                  `json:"outcome"`
		Verification    string                  `json:"artifact_verification"`
		Started         bool                    `json:"installation_started"`
		ArtifactResults []installArtifactResult `json:"artifact_results"`
	}
	step := operationStep(operation, provisionInstallStep)
	created, err := provisionCreateStepOutput(operation)
	var expected struct {
		Target struct {
			ServerID      string `json:"server_id"`
			EnvironmentID string `json:"environment_id"`
		} `json:"target"`
		ReleaseID string `json:"release_id"`
	}
	if err != nil || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &out) != nil || json.Unmarshal(step.ResolvedInput, &expected) != nil {
		return false
	}
	if (out.Outcome != "installed" && out.Outcome != "already_installed") || out.Verification != "verified" || !installArtifactsVerified(out.ArtifactResults) {
		return false
	}
	return out.ReleaseID == expected.ReleaseID && out.Target.ServerID == expected.Target.ServerID &&
		out.Target.EnvironmentID == expected.Target.EnvironmentID && equalJSON(out.Target.InstanceRef, created.InstanceRef)
}

type installArtifactResult struct {
	ArtifactID string          `json:"artifact_id"`
	Result     string          `json:"result"`
	Expected   json.RawMessage `json:"expected_checksum"`
	Observed   json.RawMessage `json:"observed_checksum"`
}

// installArtifactsVerified requires at least one artifact result, each verified with matching pinned and observed checksums.
func installArtifactsVerified(results []installArtifactResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, item := range results {
		if item.ArtifactID == "" || item.Result != "verified" || len(item.Expected) == 0 || !equalJSON(item.Expected, item.Observed) {
			return false
		}
	}
	return true
}
func provisionUnitCreated(operation store.OperationRecord) bool {
	var out struct {
		UnitRef json.RawMessage `json:"unit_ref"`
		Target  struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID string          `json:"release_id"`
		Checksum  json.RawMessage `json:"definition_checksum"`
		Outcome   string          `json:"outcome"`
		State     string          `json:"state"`
		Started   bool            `json:"started_by_request"`
		Preserved bool            `json:"persistent_data_preserved"`
	}
	step := operationStep(operation, provisionCreateUnitStep)
	created, err := provisionCreateStepOutput(operation)
	var expected struct {
		UnitRef json.RawMessage `json:"unit_ref"`
		Target  struct {
			ServerID      string `json:"server_id"`
			EnvironmentID string `json:"environment_id"`
		} `json:"target"`
		ReleaseID      string `json:"release_id"`
		UnitDefinition struct {
			Checksum json.RawMessage `json:"checksum"`
		} `json:"unit_definition"`
	}
	if err != nil || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &out) != nil || json.Unmarshal(step.ResolvedInput, &expected) != nil {
		return false
	}
	if !isObject(out.UnitRef) || !equalJSON(out.UnitRef, expected.UnitRef) ||
		!provisionUnitCreatedWithoutStart(out.Outcome, out.State, out.Started, out.Preserved) {
		return false
	}
	if out.ReleaseID != expected.ReleaseID || out.Target.ServerID != expected.Target.ServerID ||
		out.Target.EnvironmentID != expected.Target.EnvironmentID {
		return false
	}
	return equalJSON(out.Target.InstanceRef, created.InstanceRef) && equalJSON(out.Checksum, expected.UnitDefinition.Checksum)
}
func provisionUnitCreatedWithoutStart(outcome, state string, started, preserved bool) bool {
	if outcome != "created" && outcome != "unchanged" {
		return false
	}
	return (state == "stopped" || state == "running") && !started && preserved
}

func provisionUnitStarted(operation store.OperationRecord) bool {
	var out executionUnitStartResult
	step := operationStep(operation, provisionStartUnitStep)
	created := operationStep(operation, provisionCreateUnitStep)
	if step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &out) != nil {
		return false
	}
	return out.Outcome == "completed" && out.State == "running" &&
		equalJSON(out.UnitRef, stepField(created.Output, "unit_ref")) && equalJSON(out.UnitRef, stepField(step.Input, "unit_ref"))
}
func provisionUnitRunning(operation store.OperationRecord) bool {
	out, err := unitInspectStepOutput(operation, provisionInspectUnitStep)
	return err == nil && out.State == "running" && equalJSON(out.UnitRef, stepField(operationStep(operation, provisionStartUnitStep).Input, "unit_ref"))
}

// decodeProvisionRef decodes an object reference that names its adapter,
// environment, kind and resource.
func decodeProvisionRef(raw json.RawMessage) (provisionRef, bool) {
	var ref provisionRef
	if json.Unmarshal(raw, &ref) != nil || !isObject(raw) {
		return ref, false
	}
	return ref, ref.AdapterID != "" && ref.EnvironmentID != "" && provisionRefNamed(ref)
}
