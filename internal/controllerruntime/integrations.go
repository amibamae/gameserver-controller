package controllerruntime

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/cloudflaredns"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/velocityproxy"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

// hostingTokenSecret is the secret_refs name a hosting profile uses for its
// Vultr API token. Instance-IP backends need it to look up the instance's IP.
const hostingTokenSecret = "api_token"

// credentialResolver is the subset of credential.Resolver the integration
// adapters use.
type credentialResolver interface {
	WithCredential(reference string, use func([]byte) error) error
}

// unavailableCredentials lets Validate construct adapters without reading any
// credential: every lookup fails.
type unavailableCredentials struct{}

func (unavailableCredentials) WithCredential(string, func([]byte) error) error {
	return errors.New("credentials are not read during validation")
}

// velocityParts are the pieces of one Velocity adapter, built without
// contacting the proxy.
type velocityParts struct {
	adapter *velocityproxy.Adapter
	bridge  *checkedBridge
}

func buildVelocity(value config.Config, profile config.AdapterProfile, protocolVersion string, credentials credentialResolver) (velocityParts, error) {
	v := profile.Velocity
	timeout, _ := time.ParseDuration(v.Timeout)
	http, err := velocityproxy.NewHTTPBridge(velocityproxy.HTTPBridgeConfig{
		BaseURL: v.BridgeURL, TokenRef: profile.SecretRefs[config.VelocityBridgeTokenSecret].SecretRef,
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
		addresses.LookupIP = vultrIPLookup(profile.Velocity.EnvironmentID, hostingTokens, credentials)
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

// cloudflareRecords derives the managed record names for one DNS adapter from
// the network profiles of the servers that use it: each hostname backend gets
// backend_address, and each direct_dns join hostname gets direct_join plus
// fallback with the configured lobby values.
func cloudflareRecords(value config.Config, profileID string) map[string]cloudflaredns.ManagedRecord {
	records := map[string]cloudflaredns.ManagedRecord{}
	for _, server := range value.Servers {
		network := server.Network
		if network == nil || network.DNS != profileID {
			continue
		}
		for _, backend := range network.Backends {
			if name, ok := network.BackendHostname(server.ID, server.EnvironmentID, backend); ok && name != "" {
				records[strings.ToLower(name)] = cloudflaredns.ManagedRecord{Purposes: []cloudflaredns.Purpose{cloudflaredns.PurposeBackendAddress}}
			}
		}
		if network.JoinMode == "direct_dns" {
			fallback := map[string][]string{}
			for recordType, values := range network.FallbackRecords {
				fallback[recordType] = append([]string(nil), values...)
				sort.Strings(fallback[recordType])
			}
			records[strings.ToLower(network.JoinHostname)] = cloudflaredns.ManagedRecord{
				Purposes: []cloudflaredns.Purpose{cloudflaredns.PurposeDirectJoin, cloudflaredns.PurposeFallback},
				Fallback: fallback,
			}
		}
	}
	return records
}

func buildCloudflare(value config.Config, profile config.AdapterProfile, protocolVersion string, credentials credentialResolver) (*cloudflaredns.Adapter, error) {
	c := profile.Cloudflare
	records := cloudflareRecords(value, profile.ID)
	if len(records) == 0 {
		return nil, errors.New("no server network profile uses this DNS adapter, so it manages no records; remove it or reference it from a network profile")
	}
	return cloudflaredns.New(cloudflaredns.Config{
		AdapterID: profile.ID, EnvironmentID: c.EnvironmentID, ZoneID: c.ZoneID, ZoneName: c.ZoneName,
		APITokenRef: profile.SecretRefs[config.CloudflareAPITokenSecret].SecretRef, Records: records,
	}, protocolVersion, credentials, nil)
}
