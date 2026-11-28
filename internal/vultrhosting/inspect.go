package vultrhosting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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

// notReturnedDiagnostic explains an unknown state after a 404. A 404 can also mean a wrong token or account, so it never proves
// the instance is gone.
const notReturnedDiagnostic = "Vultr did not return this instance; it may not exist, or the API token may not see it"

// inspect reads one exact instance. A 404 is reported as state unknown, never
// destroyed. Every other failure, including 401, 403 and 429, is an error so
// the caller can retry rather than record an observation.
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
	if notFound(err) {
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

// notFound reports a 404 from Vultr. Only a 404 is read as an observation;
// 401, 403, 429 and every other failure stay errors.
func notFound(err error) bool {
	var status vultrapi.StatusError
	return errors.As(err, &status) && status == http.StatusNotFound
}

// instanceState maps Vultr's status, power_status and server_status to the
// protocol states. An instance is active only when it is running and its
// server_status is ok. A locked instance, a suspended or resizing one, or any
// value Vultr may add later stays unknown rather than being read as active.
func instanceState(instance vultrapi.Instance) string {
	switch {
	case instance.Status == "pending":
		return "creating"
	case instance.Status != "active", instance.ServerStatus == "locked":
		return "unknown"
	case instance.PowerStatus == "stopped":
		return "stopped"
	case instance.PowerStatus != "running":
		return "unknown"
	}
	return runningState(instance.ServerStatus)
}

// runningState maps the server_status of a running instance. A missing or
// unrecognized value is unknown.
func runningState(serverStatus string) string {
	switch serverStatus {
	case "ok":
		return "active"
	case "installingbooting":
		return "creating"
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
