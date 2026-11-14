package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type rejectingDestroyPlanSchema struct{ schemaRuntime }

func (rejectingDestroyPlanSchema) ValidateJSON(string, []byte) error {
	return errors.New("synthetic invalid plan input")
}

func TestServerDestroyPlanCharacterization(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	ctx := context.Background()
	operation := fixture.operation.Record
	instance := json.RawMessage(`{"instance_ref":` + startTestInstance + `}`)
	unit := json.RawMessage(`{"unit_ref":` + startTestUnit + `}`)
	want := []store.OperationStep{
		{StepID: "inspect-instance", StepType: "hosting.instance.inspect", Status: protocolstate.StepPending, AdapterID: "hosting.fake", CapabilityName: "hosting.instance.inspect", CapabilityVersion: "1", Input: instance},
		{StepID: "inspect-runtime", StepType: "execution.unit.inspect", Status: protocolstate.StepPending, AdapterID: "execution.fake", CapabilityName: "execution.unit.inspect", CapabilityVersion: "1", Input: unit},
		{StepID: "destroy-instance", StepType: "hosting.instance.destroy", Status: protocolstate.StepPending, AdapterID: "hosting.fake", CapabilityName: "hosting.instance.destroy", CapabilityVersion: "1", Input: instance},
		{StepID: "verify-instance-absence", StepType: "hosting.instance.inspect", Status: protocolstate.StepPending, AdapterID: "hosting.fake", CapabilityName: "hosting.instance.inspect", CapabilityVersion: "1", Input: instance},
	}
	steps, err := fixture.workflow.BuildPlan(ctx, operation)
	if err != nil || !reflect.DeepEqual(steps, want) {
		t.Fatalf("BuildPlan = %#v, %v, want %#v", steps, err, want)
	}

	workflow := *fixture.workflow
	workflow.schemas = rejectingDestroyPlanSchema{workflow.schemas}
	workflow.adapters = &destroyRuntimeOverride{
		adapterRuntime: workflow.adapters,
		descriptor:     destroyWithoutCapability("hosting.instance.destroy"),
	}
	assertDestroyPlanError(t, &workflow, operation, `adapter "hosting.fake" does not advertise required capability hosting.instance.destroy`)
	workflow.adapters = fixture.workflow.adapters
	assertDestroyPlanError(t, &workflow, operation, "planned inspect-instance input does not match its declared capability schema: synthetic invalid plan input")
}

func assertDestroyPlanError(t *testing.T, workflow *ServerDestroyWorkflow, operation store.OperationRecord, want string) {
	t.Helper()
	steps, err := workflow.BuildPlan(context.Background(), operation)
	if steps != nil || err == nil || err.Error() != want {
		t.Fatalf("BuildPlan = %#v, %v, want nil steps and %q", steps, err, want)
	}
}
