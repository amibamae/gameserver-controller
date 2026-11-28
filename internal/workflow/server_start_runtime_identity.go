package workflow

import (
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// runtimeIdentityCapabilityVersion is the capability version at which
// execution.unit.inspect reports runtime_id for a running unit and
// game.readiness.check reports the runtime_id its checks observed. server.start
// binds readiness to the started runtime, so it requires this version of both
// before it dispatches a unit start.
const runtimeIdentityCapabilityVersion = "2"

// runtimeIdentitySteps names the server.start steps that must report runtime
// identity, with the capability each one dispatches.
var runtimeIdentitySteps = []struct{ stepID, capability string }{
	{stepID: startVerifyRuntimeStep, capability: "execution.unit.inspect"},
	{stepID: startReadinessStep, capability: "game.readiness.check"},
}

// checkRuntimeIdentityCapabilities requires every runtime-identity step in
// steps to record the runtime-identity capability version.
func checkRuntimeIdentityCapabilities(steps []store.OperationStep) error {
	planned := store.OperationRecord{Steps: steps}
	for _, required := range runtimeIdentitySteps {
		version := operationStep(planned, required.stepID).CapabilityVersion
		if version != runtimeIdentityCapabilityVersion {
			return fmt.Errorf("server.start requires %s capability version %s, which reports runtime_id; step %q uses version %q",
				required.capability, runtimeIdentityCapabilityVersion, required.stepID, version)
		}
	}
	return nil
}

// requireRuntimeIdentity returns planned steps only when they were encoded
// without error and record the runtime-identity capability versions.
func requireRuntimeIdentity(steps []store.OperationStep, err error) ([]store.OperationStep, error) {
	if err != nil {
		return nil, err
	}
	if err := checkRuntimeIdentityCapabilities(steps); err != nil {
		return nil, err
	}
	return steps, nil
}
