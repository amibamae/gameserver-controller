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
	addresses       AddressResolver
	now             func() time.Time
}

// New constructs a Velocity adapter. protocolVersion must match the loaded
// protocol registry.
func New(config Config, protocolVersion string, bridge Bridge, addresses AddressResolver) (*Adapter, error) {
	return newAdapter(config, protocolVersion, bridge, addresses, time.Now)
}

func newAdapter(config Config, protocolVersion string, bridge Bridge, addresses AddressResolver, now func() time.Time) (*Adapter, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if protocolVersion == "" || bridge == nil || addresses == nil || now == nil {
		return nil, errors.New("protocol version, proxy bridge, and backend address resolver are required")
	}
	return &Adapter{config: config, protocolVersion: protocolVersion, bridge: bridge, addresses: addresses, now: now}, nil
}

func validateConfig(config Config) error {
	if !validAdapterIdentity(config) {
		return errors.New("an integration adapter ID and environment are required")
	}
	if !ownsProxyInstanceRef(config) {
		return errors.New("proxy instance reference must name this adapter, its environment, and a proxy-instance ID")
	}
	return nil
}

// validAdapterIdentity reports whether the adapter ID is an integration ID
// and the environment ID is non-empty and at most 200 bytes.
func validAdapterIdentity(config Config) bool {
	return adapterIDPattern.MatchString(config.AdapterID) && config.EnvironmentID != "" && len(config.EnvironmentID) <= 200
}

// ownsProxyInstanceRef reports whether the proxy instance reference names this
// adapter, its environment, and a proxy-instance ID of at most 1024 bytes.
func ownsProxyInstanceRef(config Config) bool {
	ref := config.ProxyInstanceRef
	ownsRef := ref.AdapterID == config.AdapterID && ref.EnvironmentID == config.EnvironmentID && ref.ResourceKind == proxyInstanceKind
	return ownsRef && ref.ExternalID != "" && len(ref.ExternalID) <= 1024
}

// Invoke dispatches one schema-validated capability call.
func (a *Adapter) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if ctx == nil || a == nil {
		return adapter.InvokeResponse{}, errors.New("Velocity adapter and invocation context are required")
	}
	handler, ok := a.handlers()[request.Params.Capability]
	if !ok {
		return adapter.InvokeResponse{}, errors.New("unsupported Velocity capability")
	}
	output, outcome, err := handler(ctx, request.Params.Input)
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

// capabilityHandler runs one capability on its raw, schema-validated input.
type capabilityHandler func(context.Context, json.RawMessage) (any, adapter.Outcome, error)

func (a *Adapter) handlers() map[string]capabilityHandler {
	return map[string]capabilityHandler{
		"integration.proxy.backend.inspect":         a.inspectBackend,
		"integration.proxy.backend.register":        a.registerBackend,
		"integration.proxy.backend.drain":           a.changeHandler(a.bridge.DrainBackend, Draining),
		"integration.proxy.backend.unregister":      a.changeHandler(a.bridge.UnregisterBackend, Unregistered),
		"integration.proxy.backend.players.inspect": a.inspectPlayers,
		"integration.proxy.session.inspect":         a.inspectSession,
		"integration.proxy.cohort.inspect":          a.inspectCohort,
		"integration.velocity.players.transfer":     a.transferPlayers,
	}
}
