// Package velocityproxy implements the controller's Velocity proxy
// integration adapter.
//
// The adapter enforces the protocol rules for backend routing and player
// transfer: readiness assertions must be current and bound to the exact
// server, environment, and instance; routing state is confirmed only from an
// observation of the exact backend and instance; and every requested session
// gets one explicit transfer outcome. Communication with the running Velocity
// proxy goes through the Bridge interface, so the transport (for example a
// companion Velocity plugin) is a separate concern.
package velocityproxy

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

const (
	contractVersion        = "integration.proxy.v1-draft"
	implementationVersion  = "0.1.0"
	capabilityVersion      = "1"
	proxyInstanceKind      = "proxy-instance"
	maxSessionsPerTransfer = 100

	permissionBackendManage  = "proxy.backend.manage"
	permissionSessionInspect = "proxy.session.inspect"
)

var adapterIDPattern = regexp.MustCompile(`^integration\.[a-z0-9][a-z0-9.-]{0,147}$`)

// ErrNotAttempted lets a Bridge report that a mutating call definitely did
// not reach the proxy, so the adapter can report a failure instead of an
// unknown outcome. Any other Bridge error from a mutating call is treated as
// an unknown outcome that requires inspection before retry.
var ErrNotAttempted = errors.New("proxy action was not attempted")

// ResourceRef is the protocol's external resource reference.
type ResourceRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

// BackendTarget names one proxy backend bound to one logical server,
// environment, and exact hosting instance.
type BackendTarget struct {
	ServerID      string      `json:"server_id"`
	EnvironmentID string      `json:"environment_id"`
	BackendID     string      `json:"backend_id"`
	InstanceRef   ResourceRef `json:"instance_ref"`
}

// ReadinessAssertion is the controller-supplied readiness evidence.
type ReadinessAssertion struct {
	AssertionID         string      `json:"assertion_id"`
	ServerID            string      `json:"server_id"`
	EnvironmentID       string      `json:"environment_id"`
	InstanceRef         ResourceRef `json:"instance_ref"`
	ReadinessCheckSetID string      `json:"readiness_check_set_id"`
	ObservedAt          time.Time   `json:"observed_at"`
	ExpiresAt           time.Time   `json:"expires_at"`
}

// RegistrationState is the proxy's view of one backend name.
type RegistrationState string

const (
	Registered   RegistrationState = "registered"
	Draining     RegistrationState = "draining"
	Unregistered RegistrationState = "unregistered"
	Unknown      RegistrationState = "unknown"
)

// BackendObservation is what the proxy reports for one backend name.
type BackendObservation struct {
	Registration RegistrationState
	// InstanceRef is the hosting instance the proxy currently routes this
	// backend name to, or nil when it is not bound to any known instance.
	InstanceRef *ResourceRef
	// Routable reports whether the proxy would send new connections to the
	// backend. It is ignored unless Registration is Registered.
	Routable bool
	// ActiveSessions is the number of connected sessions, when known.
	ActiveSessions *int64
	ObservedAt     time.Time
}

// ConnectionState is the proxy's view of one session.
type ConnectionState string

const (
	Connected      ConnectionState = "connected"
	Disconnected   ConnectionState = "disconnected"
	SessionUnknown ConnectionState = "unknown"
)

// SessionObservation is what the proxy reports for one opaque session.
type SessionObservation struct {
	State ConnectionState
	// BackendID is the backend the session is connected to, when connected.
	BackendID string
	// WaitingCohortID is the waiting cohort the session belongs to, if any.
	WaitingCohortID string
	ObservedAt      time.Time
}

// Bridge is the transport to one running Velocity proxy instance. Methods
// must not return player names, account UUIDs, IP addresses, or chat, and
// their errors must not embed session identifiers.
type Bridge interface {
	InspectBackend(ctx context.Context, backendID string) (BackendObservation, error)
	RegisterBackend(ctx context.Context, target BackendTarget) error
	DrainBackend(ctx context.Context, target BackendTarget) error
	UnregisterBackend(ctx context.Context, target BackendTarget) error
	InspectSession(ctx context.Context, sessionID string) (SessionObservation, error)
	// TransferSession moves one connected session to the destination backend
	// and returns nil only after the proxy confirms the connection.
	TransferSession(ctx context.Context, sessionID, destinationBackendID string) error
}

// Config contains the non-secret settings for one Velocity proxy instance.
type Config struct {
	AdapterID     string
	EnvironmentID string
	// ProxyInstanceRef identifies the proxy instance this adapter controls.
	ProxyInstanceRef ResourceRef
}

// Adapter implements adapter.Implementation for one Velocity proxy.
type Adapter struct {
	config          Config
	protocolVersion string
	bridge          Bridge
	now             func() time.Time
}

// New constructs a Velocity adapter. protocolVersion must match the loaded
// protocol registry.
func New(config Config, protocolVersion string, bridge Bridge) (*Adapter, error) {
	return newAdapter(config, protocolVersion, bridge, time.Now)
}

func newAdapter(config Config, protocolVersion string, bridge Bridge, now func() time.Time) (*Adapter, error) {
	if !adapterIDPattern.MatchString(config.AdapterID) || config.EnvironmentID == "" || len(config.EnvironmentID) > 200 {
		return nil, errors.New("an integration adapter ID and environment are required")
	}
	ref := config.ProxyInstanceRef
	if ref.AdapterID != config.AdapterID || ref.EnvironmentID != config.EnvironmentID || ref.ResourceKind != proxyInstanceKind || ref.ExternalID == "" || len(ref.ExternalID) > 1024 {
		return nil, errors.New("proxy instance reference must name this adapter, its environment, and a proxy-instance ID")
	}
	if protocolVersion == "" || bridge == nil || now == nil {
		return nil, errors.New("protocol version and proxy bridge are required")
	}
	return &Adapter{config: config, protocolVersion: protocolVersion, bridge: bridge, now: now}, nil
}

// Descriptor advertises the proxy backend lifecycle, session inspection, and
// Velocity player transfer capabilities.
func (a *Adapter) Descriptor() adapter.Descriptor {
	schema := func(name string) string {
		return "urn:gameserver-controller:" + a.protocolVersion + ":integrations:" + name
	}
	capability := func(name, schemaName string, mutating bool, permission string) adapter.Capability {
		return adapter.Capability{
			Name: name, Version: capabilityVersion, Mutating: mutating,
			InputSchemaURI: schema(schemaName + "-input"), OutputSchemaURI: schema(schemaName + "-output"),
			RequiredPermissions: []string{permission},
		}
	}
	transfer := adapter.Capability{
		Name: "integration.velocity.players.transfer", Version: capabilityVersion, Mutating: true,
		InputSchemaURI: schema("velocity-player-transfer-request"), OutputSchemaURI: schema("velocity-player-transfer-result"),
		RequiredPermissions: []string{permissionBackendManage},
		Constraints: map[string]any{
			"max_sessions_per_invocation":                 maxSessionsPerTransfer,
			"requires_current_readiness_assertion":        true,
			"reconciliation_required_for_unknown_outcome": true,
		},
	}
	return adapter.Descriptor{
		AdapterID: a.config.AdapterID, Category: adapter.CategoryIntegration, DisplayName: "Velocity proxy integration",
		ImplementationVersion: implementationVersion, ProtocolVersions: []string{a.protocolVersion},
		ContractVersions:    []string{contractVersion},
		RequiredPermissions: []string{permissionBackendManage, permissionSessionInspect},
		Capabilities: []adapter.Capability{
			capability("integration.proxy.backend.drain", "proxy-backend-drain", true, permissionBackendManage),
			capability("integration.proxy.backend.inspect", "proxy-backend-inspect", false, permissionBackendManage),
			capability("integration.proxy.backend.players.inspect", "proxy-backend-players-inspect", false, permissionBackendManage),
			capability("integration.proxy.backend.register", "proxy-backend-register", true, permissionBackendManage),
			capability("integration.proxy.backend.unregister", "proxy-backend-unregister", true, permissionBackendManage),
			capability("integration.proxy.session.inspect", "proxy-session-inspect", false, permissionSessionInspect),
			transfer,
		},
		Constraints: map[string]any{"proxy_platform": "Velocity", "requires_proxy_session_identity": true},
	}
}

// Invoke dispatches one schema-validated capability call.
func (a *Adapter) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if ctx == nil || a == nil {
		return adapter.InvokeResponse{}, errors.New("Velocity adapter and invocation context are required")
	}
	var (
		output  any
		outcome adapter.Outcome
		err     error
	)
	input := request.Params.Input
	switch request.Params.Capability {
	case "integration.proxy.backend.inspect":
		output, outcome, err = a.inspectBackend(ctx, input)
	case "integration.proxy.backend.register":
		output, outcome, err = a.registerBackend(ctx, input)
	case "integration.proxy.backend.drain":
		output, outcome, err = a.changeBackend(ctx, input, a.bridge.DrainBackend, Draining)
	case "integration.proxy.backend.unregister":
		output, outcome, err = a.changeBackend(ctx, input, a.bridge.UnregisterBackend, Unregistered)
	case "integration.proxy.backend.players.inspect":
		output, outcome, err = a.inspectPlayers(ctx, input)
	case "integration.proxy.session.inspect":
		output, outcome, err = a.inspectSession(ctx, input)
	case "integration.velocity.players.transfer":
		output, outcome, err = a.transferPlayers(ctx, input)
	default:
		return adapter.InvokeResponse{}, errors.New("unsupported Velocity capability")
	}
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return adapter.InvokeResponse{}, errors.New("could not encode Velocity result")
	}
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{
		Outcome: outcome, Output: encoded, ObservedAt: a.now().UTC(),
	}}, nil
}

type backendInput struct {
	Target BackendTarget `json:"target"`
}

type registerInput struct {
	Target             BackendTarget      `json:"target"`
	ReadinessAssertion ReadinessAssertion `json:"readiness_assertion"`
}

type playersInput struct {
	Target            BackendTarget `json:"target"`
	MaximumAgeSeconds int           `json:"maximum_age_seconds"`
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

type playersOutput struct {
	Target             BackendTarget `json:"target"`
	ObservationState   string        `json:"observation_state"`
	ActiveSessionCount *int64        `json:"active_session_count,omitempty"`
	ObservedAt         *time.Time    `json:"observed_at,omitempty"`
}

func decode(input json.RawMessage, target any) error {
	if err := json.Unmarshal(input, target); err != nil {
		return errors.New("invalid Velocity capability input")
	}
	return nil
}

// checkTarget rejects targets outside this adapter's environment. The
// registry has already validated the input schema.
func (a *Adapter) checkTarget(target BackendTarget) error {
	if target.EnvironmentID != a.config.EnvironmentID || target.InstanceRef.EnvironmentID != target.EnvironmentID {
		return errors.New("backend target is outside this Velocity adapter's environment")
	}
	return nil
}

// boundState maps a proxy observation to the protocol state of the exact
// requested instance. A backend name bound to another instance never reports
// the requested instance as routable.
func boundState(target BackendTarget, observed BackendObservation) (RegistrationState, string) {
	switch observed.Registration {
	case Unregistered:
		return Unregistered, "not_routable"
	case Registered, Draining:
		if observed.InstanceRef == nil || *observed.InstanceRef != target.InstanceRef {
			return Unknown, "not_routable"
		}
		if observed.Registration == Draining {
			return Draining, "not_routable"
		}
		if observed.Routable {
			return Registered, "routable"
		}
		return Registered, "not_routable"
	default:
		return Unknown, "unknown"
	}
}

func (a *Adapter) observedAt(observed time.Time) time.Time {
	if observed.IsZero() {
		return a.now().UTC()
	}
	return observed.UTC()
}

func (a *Adapter) inspectBackend(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var input backendInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	if err := a.checkTarget(input.Target); err != nil {
		return nil, "", err
	}
	observed, err := a.bridge.InspectBackend(ctx, input.Target.BackendID)
	if err != nil {
		return backendStateOutput{Target: input.Target, RegistrationState: string(Unknown), Routability: "unknown", ObservedAt: a.now().UTC()}, adapter.OutcomeCompleted, nil
	}
	state, routability := boundState(input.Target, observed)
	output := backendStateOutput{Target: input.Target, RegistrationState: string(state), Routability: routability, ObservedAt: a.observedAt(observed.ObservedAt)}
	if (state == Registered || state == Draining) && observed.ActiveSessions != nil && *observed.ActiveSessions >= 0 {
		count := *observed.ActiveSessions
		output.ActiveSessionCount = &count
	}
	return output, adapter.OutcomeCompleted, nil
}

// readinessProblem returns a reason code when the assertion cannot authorize
// routing to the exact target, or "" when it is current and bound.
func (a *Adapter) readinessProblem(assertion ReadinessAssertion, serverID, environmentID string, instance ResourceRef) string {
	if assertion.ServerID != serverID || assertion.EnvironmentID != environmentID || assertion.InstanceRef != instance {
		return "readiness_assertion_mismatch"
	}
	now := a.now()
	if assertion.ObservedAt.IsZero() || assertion.ExpiresAt.IsZero() || !assertion.ExpiresAt.After(assertion.ObservedAt) {
		return "readiness_assertion_invalid"
	}
	if assertion.ObservedAt.After(now) {
		return "readiness_assertion_not_yet_valid"
	}
	if !assertion.ExpiresAt.After(now) {
		return "readiness_assertion_expired"
	}
	return ""
}

func (a *Adapter) registerBackend(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var input registerInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	if err := a.checkTarget(input.Target); err != nil {
		return nil, "", err
	}
	if reason := a.readinessProblem(input.ReadinessAssertion, input.Target.ServerID, input.Target.EnvironmentID, input.Target.InstanceRef); reason != "" {
		return a.rejected(ctx, input.Target, reason), adapter.OutcomeCompleted, nil
	}
	return a.applyAndConfirm(ctx, input.Target, a.bridge.RegisterBackend, Registered)
}

func (a *Adapter) changeBackend(ctx context.Context, raw json.RawMessage, action func(context.Context, BackendTarget) error, want RegistrationState) (any, adapter.Outcome, error) {
	var input backendInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	if err := a.checkTarget(input.Target); err != nil {
		return nil, "", err
	}
	return a.applyAndConfirm(ctx, input.Target, action, want)
}

// rejected reports a refused request together with the currently observed
// state, or unknown state when the proxy cannot be inspected.
func (a *Adapter) rejected(ctx context.Context, target BackendTarget, reason string) backendActionOutput {
	output := backendActionOutput{Target: target, RequestOutcome: "rejected", RegistrationState: string(Unknown), Routability: "unknown", ObservedAt: a.now().UTC(), ReasonCode: reason}
	if observed, err := a.bridge.InspectBackend(ctx, target.BackendID); err == nil {
		state, routability := boundState(target, observed)
		output.RegistrationState, output.Routability, output.ObservedAt = string(state), routability, a.observedAt(observed.ObservedAt)
	}
	return output
}

// applyAndConfirm performs one mutating backend action, then confirms it
// only from an observation of the exact backend and instance.
func (a *Adapter) applyAndConfirm(ctx context.Context, target BackendTarget, action func(context.Context, BackendTarget) error, want RegistrationState) (any, adapter.Outcome, error) {
	if err := action(ctx, target); err != nil {
		if errors.Is(err, ErrNotAttempted) {
			return a.rejected(ctx, target, "proxy_action_not_attempted"), adapter.OutcomeCompleted, nil
		}
		return backendActionOutput{Target: target, RequestOutcome: "outcome_unknown", RegistrationState: string(Unknown), Routability: "unknown", ObservedAt: a.now().UTC(), ReasonCode: "proxy_action_outcome_unknown"}, adapter.OutcomeUnknown, nil
	}
	observed, err := a.bridge.InspectBackend(ctx, target.BackendID)
	if err != nil {
		return backendActionOutput{Target: target, RequestOutcome: "accepted", RegistrationState: string(Unknown), Routability: "unknown", ObservedAt: a.now().UTC(), ReasonCode: "confirmation_unavailable"}, adapter.OutcomeAccepted, nil
	}
	state, routability := boundState(target, observed)
	output := backendActionOutput{Target: target, RegistrationState: string(state), Routability: routability, ObservedAt: a.observedAt(observed.ObservedAt)}
	confirmed := state == want && (want != Registered || routability == "routable")
	if confirmed {
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

func (a *Adapter) inspectPlayers(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var input playersInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	if err := a.checkTarget(input.Target); err != nil {
		return nil, "", err
	}
	unknown := playersOutput{Target: input.Target, ObservationState: "unknown"}
	observed, err := a.bridge.InspectBackend(ctx, input.Target.BackendID)
	if err != nil || observed.ActiveSessions == nil || *observed.ActiveSessions < 0 || observed.ObservedAt.IsZero() {
		return unknown, adapter.OutcomeCompleted, nil
	}
	if state, _ := boundState(input.Target, observed); state != Registered && state != Draining {
		return unknown, adapter.OutcomeCompleted, nil
	}
	if !a.fresh(observed.ObservedAt, input.MaximumAgeSeconds) {
		return unknown, adapter.OutcomeCompleted, nil
	}
	count, at := *observed.ActiveSessions, observed.ObservedAt.UTC()
	return playersOutput{Target: input.Target, ObservationState: "observed", ActiveSessionCount: &count, ObservedAt: &at}, adapter.OutcomeCompleted, nil
}

// fresh reports whether an observation is not in the future and no older
// than maxAgeSeconds.
func (a *Adapter) fresh(observed time.Time, maxAgeSeconds int) bool {
	now := a.now()
	return !observed.IsZero() && !observed.After(now) && now.Sub(observed) <= time.Duration(maxAgeSeconds)*time.Second
}

type sessionInput struct {
	ProxyInstanceRef             ResourceRef `json:"proxy_instance_ref"`
	ProxySessionID               string      `json:"proxy_session_id"`
	MaximumObservationAgeSeconds int         `json:"maximum_observation_age_seconds"`
}

type sessionOutput struct {
	ProxyInstanceRef ResourceRef `json:"proxy_instance_ref"`
	ProxySessionID   string      `json:"proxy_session_id"`
	ConnectionState  string      `json:"connection_state"`
	CurrentBackendID string      `json:"current_backend_id,omitempty"`
	ObservedAt       time.Time   `json:"observed_at"`
}

func (a *Adapter) inspectSession(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var input sessionInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	if input.ProxyInstanceRef != a.config.ProxyInstanceRef {
		return nil, "", errors.New("session inspection targets a different proxy instance")
	}
	output := sessionOutput{ProxyInstanceRef: input.ProxyInstanceRef, ProxySessionID: input.ProxySessionID, ConnectionState: "unknown", ObservedAt: a.now().UTC()}
	observed, err := a.bridge.InspectSession(ctx, input.ProxySessionID)
	if err != nil || !a.fresh(observed.ObservedAt, input.MaximumObservationAgeSeconds) {
		return output, adapter.OutcomeCompleted, nil
	}
	output.ObservedAt = observed.ObservedAt.UTC()
	switch {
	case observed.State == Connected && observed.BackendID != "" && len(observed.BackendID) <= 200:
		output.ConnectionState, output.CurrentBackendID = "connected", observed.BackendID
	case observed.State == Disconnected:
		output.ConnectionState = "disconnected"
	}
	return output, adapter.OutcomeCompleted, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}
