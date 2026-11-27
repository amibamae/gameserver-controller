package vultrhosting

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

// instanceRef is the protocol's external-resource-ref for one instance.
type instanceRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

type inspectInput struct {
	InstanceRef instanceRef `json:"instance_ref"`
}

type inspectOutput struct {
	InstanceRef     instanceRef `json:"instance_ref"`
	State           string      `json:"state"`
	RegionID        string      `json:"region_id,omitempty"`
	PlanID          string      `json:"plan_id,omitempty"`
	SafeDiagnostics []string    `json:"safe_diagnostics,omitempty"`
	ObservedAt      string      `json:"observed_at"`
}

// notReturnedDiagnostic explains an unknown state after Vultr refused the
// lookup. A 404 can also mean a wrong token or account, so it never proves
// the instance is gone.
const notReturnedDiagnostic = "Vultr did not return this instance; it may not exist, or the API token may not see it"

// inspect reads one exact instance. Vultr refusing the lookup is reported as
// state unknown; transport and response failures are returned as errors so
// the caller can retry.
func (a *Adapter) inspect(ctx context.Context, input json.RawMessage) (any, error) {
	var request inspectInput
	if err := decodeStrict(input, &request); err != nil {
		return nil, errors.New("invalid Vultr instance inspect input")
	}
	if !a.boundRef(request.InstanceRef) {
		return nil, errors.New("instance reference is not bound to this Vultr adapter and environment")
	}
	var instance vultrapi.Instance
	err := a.withToken(func(token []byte) error {
		var readErr error
		instance, readErr = a.client.GetInstance(ctx, token, request.InstanceRef.ExternalID)
		return readErr
	})
	output := inspectOutput{InstanceRef: request.InstanceRef, ObservedAt: a.observedAt()}
	if errors.Is(err, vultrapi.ErrProviderReject) {
		output.State, output.SafeDiagnostics = "unknown", []string{notReturnedDiagnostic}
		return output, nil
	}
	if err != nil {
		return nil, errors.New("Vultr instance inspection failed; the instance state is unknown")
	}
	if instance.ID != request.InstanceRef.ExternalID {
		return nil, errors.New("Vultr returned a different instance than the one requested")
	}
	output.State = instanceState(instance)
	output.RegionID, output.PlanID = boundedID(instance.Region), boundedID(instance.Plan)
	return output, nil
}

// boundRef reports a reference to an instance in this adapter's environment.
func (a *Adapter) boundRef(ref instanceRef) bool {
	return ref.AdapterID == a.config.AdapterID && ref.EnvironmentID == a.config.EnvironmentID &&
		ref.ResourceKind == resourceKind && ref.ExternalID != ""
}

// instanceState maps Vultr's status and power status to the protocol states.
// Anything Vultr may add later, or a suspended or resizing instance, stays
// unknown rather than being read as active.
func instanceState(instance vultrapi.Instance) string {
	if instance.Status == "pending" {
		return "creating"
	}
	if instance.Status != "active" {
		return "unknown"
	}
	switch instance.PowerStatus {
	case "running":
		return "active"
	case "stopped":
		return "stopped"
	default:
		return "unknown"
	}
}

// boundedID returns a provider ID the output schema accepts, or "" to omit it.
func boundedID(id string) string {
	if len(id) > 200 {
		return ""
	}
	return id
}

func (a *Adapter) observedAt() string {
	return a.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
}
