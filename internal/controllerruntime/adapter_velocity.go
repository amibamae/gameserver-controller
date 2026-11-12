package controllerruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
	"github.com/amibamae/gameserver-controller/internal/velocityproxy"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

// Velocity proxy integration adapter (`implementation: velocity`).

const velocityBridgeTokenSecret = "bridge_token"

// hostingTokenSecret is the secret_refs name a hosting profile uses for its
// Vultr API token. Instance-IP backends need it to look up the instance's IP.
const hostingTokenSecret = "api_token"

// VelocitySettings is the `velocity:` block. The bridge token comes from
// secret_refs.
type VelocitySettings struct {
	EnvironmentID   string `yaml:"environment_id"`
	BridgeURL       string `yaml:"bridge_url"`
	ProxyInstanceID string `yaml:"proxy_instance_id"`
	Timeout         string `yaml:"timeout"`
}

func (v *VelocitySettings) BoundEnvironment() string { return v.EnvironmentID }

func (v *VelocitySettings) Validate(check config.SettingsCheck) {
	check.Require(check.KnownEnvironment(v.EnvironmentID), "environment_id must name a configured environment")
	check.Require(validBridgeURL(v.BridgeURL), "bridge_url must be an https URL, or http to a loopback address, with no path, query, or user information")
	check.Require(strings.TrimSpace(v.ProxyInstanceID) != "" && len(v.ProxyInstanceID) <= 1024, "proxy_instance_id is required")
	if v.Timeout != "" {
		timeout, err := time.ParseDuration(v.Timeout)
		check.Require(err == nil && timeout > 0 && timeout <= 2*time.Minute, "timeout must be greater than zero and no more than 2m")
	}
}

// validBridgeURL mirrors velocityproxy.NewHTTPBridge: https anywhere, plain
// http only to loopback, and no user information, path, query, or fragment.
func validBridgeURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		ip := net.ParseIP(u.Hostname())
		return (ip != nil && ip.IsLoopback()) || strings.EqualFold(u.Hostname(), "localhost")
	}
	return false
}

func init() {
	registerAdapter(config.Implementation{
		Name: "velocity", Category: "integration", Secrets: []string{velocityBridgeTokenSecret},
		NewSettings: func() config.ImplementationSettings { return &VelocitySettings{} },
	}, adapterBuilder{
		check: func(b adapterBuild) error {
			_, err := buildVelocity(b.value, b.profile, b.protocolVersion, unavailableCredentials{})
			return err
		},
		build: func(b adapterBuild) (builtAdapter, error) {
			credentials, err := b.credentials()
			if err != nil {
				return builtAdapter{}, err
			}
			parts, err := buildVelocity(b.value, b.profile, b.protocolVersion, credentials)
			if err != nil {
				return builtAdapter{}, err
			}
			reachable, err := parts.bridge.startupCheck(b.ctx)
			if err != nil {
				return builtAdapter{}, err
			}
			initial := integrationmonitor.Up
			if !reachable {
				initial = integrationmonitor.Unreachable
			}
			v, _ := config.SettingsOf[*VelocitySettings](b.profile)
			return builtAdapter{implementation: parts.adapter, unreachableProxy: !reachable, monitor: &integrationmonitor.Target{
				Subject: integrationmonitor.Subject{AdapterID: b.profile.ID, EnvironmentID: v.EnvironmentID, ResourceKind: "proxy-instance", ExternalID: v.ProxyInstanceID},
				Probe:   parts.bridge.Probe, Initial: initial,
			}}, nil
		},
	})
}

// velocityParts are the pieces of one Velocity adapter, built without
// contacting the proxy.
type velocityParts struct {
	adapter *velocityproxy.Adapter
	bridge  *checkedBridge
}

func buildVelocity(value config.Config, profile config.AdapterProfile, protocolVersion string, credentials credentialResolver) (velocityParts, error) {
	v, ok := config.SettingsOf[*VelocitySettings](profile)
	if !ok {
		return velocityParts{}, errors.New("velocity settings are missing")
	}
	timeout, _ := time.ParseDuration(v.Timeout)
	http, err := velocityproxy.NewHTTPBridge(velocityproxy.HTTPBridgeConfig{
		BaseURL: v.BridgeURL, TokenRef: profile.SecretRefs[velocityBridgeTokenSecret].SecretRef,
		ProxyInstanceID: v.ProxyInstanceID, Timeout: timeout,
	}, credentials, nil)
	if err != nil {
		return velocityParts{}, err
	}
	addresses := velocityAddresses(value, profile, credentials)
	if len(addresses.Backends) > 0 {
		if err := addresses.Validate(); err != nil {
			return velocityParts{}, err
		}
	}
	bridge := &checkedBridge{inner: http}
	adapter, err := velocityproxy.New(velocityproxy.Config{
		AdapterID: profile.ID, EnvironmentID: v.EnvironmentID,
		ProxyInstanceRef: velocityproxy.ResourceRef{AdapterID: profile.ID, EnvironmentID: v.EnvironmentID, ResourceKind: "proxy-instance", ExternalID: v.ProxyInstanceID},
	}, protocolVersion, bridge, addresses)
	if err != nil {
		return velocityParts{}, err
	}
	return velocityParts{adapter: adapter, bridge: bridge}, nil
}

// velocityAddresses collects the backends of every server routed through this
// proxy. Hostname backends use the profile's naming scheme; instance-IP
// backends look the IP up through the server's hosting provider.
func velocityAddresses(value config.Config, profile config.AdapterProfile, credentials credentialResolver) velocityproxy.ConfiguredAddresses {
	addresses := velocityproxy.ConfiguredAddresses{Backends: map[string]velocityproxy.BackendAddressConfig{}}
	hostingTokens := map[string]string{} // hosting adapter ID -> Vultr token reference
	usesInstanceIP := false
	for _, server := range value.Servers {
		network := server.Network
		if network == nil || network.Proxy != profile.ID {
			continue
		}
		for _, backend := range network.Backends {
			setting := velocityproxy.BackendAddressConfig{Port: uint16(backend.Port)}
			if name, ok := network.BackendHostname(server.ID, server.EnvironmentID, backend); ok {
				setting.Mode, setting.Hostname = velocityproxy.AddressHostname, name
			} else {
				setting.Mode = velocityproxy.AddressInstanceIP
				usesInstanceIP = true
				hostingTokens[server.Adapters.Hosting] = hostingToken(value, server.Adapters.Hosting)
			}
			addresses.Backends[backend.ID] = setting
		}
	}
	if usesInstanceIP {
		addresses.LookupIP = vultrIPLookup(config.BoundEnvironment(profile), hostingTokens, credentials)
	}
	return addresses
}

func hostingToken(value config.Config, hostingID string) string {
	for _, profile := range value.Adapters {
		if profile.ID == hostingID {
			return profile.SecretRefs[hostingTokenSecret].SecretRef
		}
	}
	return ""
}

// vultrIPLookup returns the instance's current public IP from Vultr. It fails
// closed for an instance in another environment, an instance owned by a
// hosting adapter that no routed server uses, or a hosting profile without a
// Vultr token.
func vultrIPLookup(environmentID string, tokens map[string]string, credentials credentialResolver) velocityproxy.InstanceIPLookup {
	client := vultrapi.NewClient(nil)
	return func(ctx context.Context, instance velocityproxy.ResourceRef) (netip.Addr, error) {
		reference, known := tokens[instance.AdapterID]
		switch {
		case instance.EnvironmentID != environmentID || instance.ResourceKind != "instance" || instance.ExternalID == "":
			return netip.Addr{}, errors.New("instance reference is not a hosting instance in this proxy's environment")
		case !known:
			return netip.Addr{}, errors.New("instance belongs to a hosting adapter that no server on this proxy uses")
		case reference == "":
			return netip.Addr{}, errors.New("hosting profile has no api_token for instance IP lookup")
		}
		var address netip.Addr
		err := credentials.WithCredential(reference, func(token []byte) error {
			var lookupErr error
			address, lookupErr = client.InstanceMainIP(ctx, token, instance.ExternalID)
			return lookupErr
		})
		return address, err
	}
}

// checkedBridge verifies the bridge identity before the first proxy call. A
// wrong identity is refused at startup; an unreachable bridge is retried
// before each call, and calls fail without acting until it answers.
type checkedBridge struct {
	inner    *velocityproxy.HTTPBridge
	mu       sync.Mutex
	verified bool
}

// startupCheck runs CheckInfo once at startup. It returns an error only when
// the controller must not start; an unreachable bridge is reported as
// reachable=false.
func (b *checkedBridge) startupCheck(ctx context.Context) (reachable bool, err error) {
	err = b.ensure(ctx)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, velocityproxy.ErrBridgeUnreachable):
		return false, nil
	default:
		return false, err
	}
}

// Probe re-runs the identity check for the outage monitor. Any failure clears
// the verified flag, so proxy calls check the identity again before acting.
func (b *checkedBridge) Probe(ctx context.Context) integrationmonitor.Result {
	err := b.inner.CheckInfo(ctx)
	b.mu.Lock()
	b.verified = err == nil
	b.mu.Unlock()
	switch {
	case err == nil:
		return integrationmonitor.Up
	case errors.Is(err, velocityproxy.ErrBridgeIdentity):
		return integrationmonitor.IdentityMismatch
	default:
		return integrationmonitor.Unreachable
	}
}

func (b *checkedBridge) ensure(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.verified {
		return nil
	}
	if err := b.inner.CheckInfo(ctx); err != nil {
		// Not attempted: nothing was sent to change proxy state.
		return fmt.Errorf("%w: %w", velocityproxy.ErrNotAttempted, err)
	}
	b.verified = true
	return nil
}

func (b *checkedBridge) InspectBackend(ctx context.Context, backendID string) (velocityproxy.BackendObservation, error) {
	if err := b.ensure(ctx); err != nil {
		return velocityproxy.BackendObservation{}, err
	}
	return b.inner.InspectBackend(ctx, backendID)
}

func (b *checkedBridge) RegisterBackend(ctx context.Context, target velocityproxy.BackendTarget, address string) error {
	if err := b.ensure(ctx); err != nil {
		return err
	}
	return b.inner.RegisterBackend(ctx, target, address)
}

func (b *checkedBridge) DrainBackend(ctx context.Context, target velocityproxy.BackendTarget) error {
	if err := b.ensure(ctx); err != nil {
		return err
	}
	return b.inner.DrainBackend(ctx, target)
}

func (b *checkedBridge) UnregisterBackend(ctx context.Context, target velocityproxy.BackendTarget) error {
	if err := b.ensure(ctx); err != nil {
		return err
	}
	return b.inner.UnregisterBackend(ctx, target)
}

func (b *checkedBridge) InspectSession(ctx context.Context, sessionID string) (velocityproxy.SessionObservation, error) {
	if err := b.ensure(ctx); err != nil {
		return velocityproxy.SessionObservation{}, err
	}
	return b.inner.InspectSession(ctx, sessionID)
}

func (b *checkedBridge) TransferSession(ctx context.Context, sessionID, destinationBackendID string) error {
	if err := b.ensure(ctx); err != nil {
		return err
	}
	return b.inner.TransferSession(ctx, sessionID, destinationBackendID)
}

func (b *checkedBridge) WaitingCohort(ctx context.Context, cohortID string) ([]string, time.Time, error) {
	if err := b.ensure(ctx); err != nil {
		return nil, time.Time{}, err
	}
	return b.inner.WaitingCohort(ctx, cohortID)
}
