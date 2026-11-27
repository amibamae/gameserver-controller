package velocityproxy

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

type backendInput struct {
	Target BackendTarget `json:"target"`
}

type registerInput struct {
	Target             BackendTarget      `json:"target"`
	ReadinessAssertion ReadinessAssertion `json:"readiness_assertion"`
}

type backendStateOutput struct {
	Target             BackendTarget `json:"target"`
	RegistrationState  string        `json:"registration_state"`
	Routability        string        `json:"routability"`
	ActiveSessionCount *int64        `json:"active_session_count,omitempty"`
	ObservedAt         time.Time     `json:"observed_at"`
}

type backendActionOutput struct {
	Target            BackendTarget `json:"target"`
	RequestOutcome    string        `json:"request_outcome"`
	RegistrationState string        `json:"registration_state"`
	Routability       string        `json:"routability"`
	ObservedAt        time.Time     `json:"observed_at"`
	ReasonCode        string        `json:"reason_code,omitempty"`
}

func (input backendInput) backendTarget() BackendTarget { return input.Target }

func (input registerInput) backendTarget() BackendTarget { return input.Target }

func (a *Adapter) inspectBackend(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	input, err := decodeTargeted[backendInput](a, raw)
	if err != nil {
		return nil, "", err
	}
	observed, err := a.bridge.InspectBackend(ctx, input.Target.BackendID)
	if err != nil {
		return backendStateOutput{Target: input.Target, RegistrationState: string(Unknown), Routability: "unknown", ObservedAt: a.now().UTC()}, adapter.OutcomeCompleted, nil
	}
	view := a.viewBackend(ctx, input.Target, observed)
	output := backendStateOutput{Target: input.Target, RegistrationState: string(view.State), Routability: view.Routability, ObservedAt: view.ObservedAt}
	if view.boundToInstance() {
		output.ActiveSessionCount = knownSessionCount(observed)
	}
	return output, adapter.OutcomeCompleted, nil
}

func (a *Adapter) registerBackend(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	input, err := decodeTargeted[registerInput](a, raw)
	if err != nil {
		return nil, "", err
	}
	if reason := a.readinessProblem(input.ReadinessAssertion, input.Target.ServerID, input.Target.EnvironmentID, input.Target.InstanceRef); reason != "" {
		return a.rejected(ctx, input.Target, reason), adapter.OutcomeCompleted, nil
	}
	address, err := a.addresses.ResolveBackendAddress(ctx, input.Target)
	if err != nil {
		return a.rejected(ctx, input.Target, "backend_address_unavailable"), adapter.OutcomeCompleted, nil
	}
	register := func(ctx context.Context, target BackendTarget) error {
		return a.bridge.RegisterBackend(ctx, target, address)
	}
	return a.applyAndConfirm(ctx, input.Target, register, Registered)
}

// changeHandler returns the handler for a backend change that is confirmed
// once the proxy reports the wanted state.
func (a *Adapter) changeHandler(action func(context.Context, BackendTarget) error, want RegistrationState) capabilityHandler {
	return func(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
		return a.changeBackend(ctx, raw, action, want)
	}
}

func (a *Adapter) changeBackend(ctx context.Context, raw json.RawMessage, action func(context.Context, BackendTarget) error, want RegistrationState) (any, adapter.Outcome, error) {
	input, err := decodeTargeted[backendInput](a, raw)
	if err != nil {
		return nil, "", err
	}
	return a.applyAndConfirm(ctx, input.Target, action, want)
}

// rejected reports a refused request together with the currently observed
// state, or unknown state when the proxy cannot be inspected.
func (a *Adapter) rejected(ctx context.Context, target BackendTarget, reason string) backendActionOutput {
	output := a.unobservedAction(target, "rejected", reason)
	if observed, err := a.bridge.InspectBackend(ctx, target.BackendID); err == nil {
		output.show(a.viewBackend(ctx, target, observed))
	}
	return output
}

// show copies a bound backend view into the action output.
func (output *backendActionOutput) show(view backendView) {
	output.RegistrationState, output.Routability, output.ObservedAt = string(view.State), view.Routability, view.ObservedAt
}

// unobservedAction reports a request outcome with unknown backend state,
// stamped with the adapter's clock.
func (a *Adapter) unobservedAction(target BackendTarget, outcome, reason string) backendActionOutput {
	return backendActionOutput{Target: target, RequestOutcome: outcome, RegistrationState: string(Unknown), Routability: "unknown", ObservedAt: a.now().UTC(), ReasonCode: reason}
}

// applyAndConfirm performs one mutating backend action, then confirms it
// only from an observation of the exact backend and instance.
func (a *Adapter) applyAndConfirm(ctx context.Context, target BackendTarget, action func(context.Context, BackendTarget) error, want RegistrationState) (any, adapter.Outcome, error) {
	if err := action(ctx, target); err != nil {
		if errors.Is(err, ErrNotAttempted) {
			return a.rejected(ctx, target, "proxy_action_not_attempted"), adapter.OutcomeCompleted, nil
		}
		return a.unobservedAction(target, "outcome_unknown", "proxy_action_outcome_unknown"), adapter.OutcomeUnknown, nil
	}
	observed, err := a.bridge.InspectBackend(ctx, target.BackendID)
	if err != nil {
		return a.unobservedAction(target, "accepted", "confirmation_unavailable"), adapter.OutcomeAccepted, nil
	}
	view := a.viewBackend(ctx, target, observed)
	output := backendActionOutput{Target: target}
	output.show(view)
	if view.confirms(want) {
		output.RequestOutcome = "confirmed"
		return output, adapter.OutcomeCompleted, nil
	}
	// Accepted is not confirmation, so it never reports the backend as
	// routable, even if the proxy still routes to it.
	output.RequestOutcome = "accepted"
	output.ReasonCode = "postcondition_not_observed"
	if output.Routability == "routable" {
		output.Routability = "unknown"
	}
	return output, adapter.OutcomeAccepted, nil
}
