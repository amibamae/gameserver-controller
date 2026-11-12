package controllerruntime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/config"
)

// TransferRequirement stands for "a transfer capability such as
// integration.velocity.players.transfer" (integrations section 6). Any
// integration.<name>.players.transfer capability satisfies it.
const TransferRequirement = "integration.*.players.transfer"

var dnsRecordCapabilities = []string{"integration.dns.record.inspect", "integration.dns.record.upsert", "integration.dns.record.delete"}

// ErrNoNetworkProfile means the server has no configured network profile, so
// its requirements cannot be resolved and planning must be refused.
var ErrNoNetworkProfile = errors.New("server has no configured network profile")

// NetworkProfile is the protocol network profile
// (urn:...:integrations:network-profile).
type NetworkProfile struct {
	JoinMode       string           `json:"join_mode"`
	ProxyAdapterID string           `json:"proxy_adapter_id,omitempty"`
	DNSAdapterID   string           `json:"dns_adapter_id,omitempty"`
	JoinHostname   string           `json:"join_hostname,omitempty"`
	Backends       []NetworkBackend `json:"backends,omitempty"`
}

// NetworkBackend is one proxy backend of a network profile.
type NetworkBackend struct {
	BackendID   string `json:"backend_id"`
	AddressMode string `json:"address_mode"`
}

// CapabilityRequirement is one capability the adapter in a role must advertise.
type CapabilityRequirement struct {
	Role       string `json:"role"` // "proxy" or "dns"
	Capability string `json:"capability"`
}

// RegisteredAdapter is what planning knows about one registered adapter.
type RegisteredAdapter struct {
	EnvironmentID string
	Capabilities  map[string]bool
}

// NetworkError lists the requirements a server's network profile has that no
// adapter registered in its environment satisfies.
type NetworkError struct {
	ServerID string
	Missing  []CapabilityRequirement
}

func (e *NetworkError) Error() string {
	parts := make([]string, 0, len(e.Missing))
	for _, missing := range e.Missing {
		parts = append(parts, missing.Role+" "+missing.Capability)
	}
	return fmt.Sprintf("server %s cannot be planned: its network profile needs %s", e.ServerID, strings.Join(parts, ", "))
}

// NetworkRequirements derives the capabilities a profile needs, sorted by
// role then capability. A profile that needs nothing returns none.
func NetworkRequirements(profile NetworkProfile) []CapabilityRequirement {
	var required []CapabilityRequirement
	add := func(role string, capabilities ...string) {
		for _, capability := range capabilities {
			required = append(required, CapabilityRequirement{Role: role, Capability: capability})
		}
	}
	switch profile.JoinMode {
	case "proxy":
		// The bridge waits players in a cohort, so the cohort, session, and
		// transfer capabilities are always required in proxy mode.
		add("proxy", "integration.proxy.backend.inspect", "integration.proxy.backend.register", "integration.proxy.backend.drain",
			"integration.proxy.backend.unregister", "integration.proxy.cohort.inspect", "integration.proxy.session.inspect", TransferRequirement)
		for _, backend := range profile.Backends {
			if backend.AddressMode == "hostname" {
				add("dns", dnsRecordCapabilities...)
				break
			}
		}
	case "direct_dns":
		add("dns", dnsRecordCapabilities...)
	}
	sortRequirements(required)
	return required
}

// MissingCapabilities returns the requirements not met by the adapter named
// for each role and registered in environmentID. It never treats a missing
// capability as optional.
func MissingCapabilities(profile NetworkProfile, environmentID string, lookup func(adapterID string) (RegisteredAdapter, bool)) []CapabilityRequirement {
	roleAdapter := map[string]string{"proxy": profile.ProxyAdapterID, "dns": profile.DNSAdapterID}
	missing := []CapabilityRequirement{}
	for _, requirement := range NetworkRequirements(profile) {
		registered, ok := lookup(roleAdapter[requirement.Role])
		if !ok || registered.EnvironmentID == "" || registered.EnvironmentID != environmentID || !satisfies(registered.Capabilities, requirement.Capability) {
			missing = append(missing, requirement)
		}
	}
	return missing
}

func satisfies(capabilities map[string]bool, requirement string) bool {
	if requirement != TransferRequirement {
		return capabilities[requirement]
	}
	for name := range capabilities {
		if strings.HasPrefix(name, "integration.") && strings.HasSuffix(name, ".players.transfer") && strings.Count(name, ".") == 3 {
			return true
		}
	}
	return false
}

func sortRequirements(values []CapabilityRequirement) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Role != values[j].Role {
			return values[i].Role < values[j].Role
		}
		return values[i].Capability < values[j].Capability
	})
}

// ProfileFromConfig converts a configured server's network block. A server
// without one reaches players without the controller (join mode none).
func ProfileFromConfig(server config.ServerProfile) NetworkProfile {
	network := server.Network
	if network == nil {
		return NetworkProfile{JoinMode: "none"}
	}
	profile := NetworkProfile{JoinMode: network.JoinMode, ProxyAdapterID: network.Proxy, DNSAdapterID: network.DNS, JoinHostname: network.JoinHostname}
	for _, backend := range network.Backends {
		mode := "instance_ip"
		if _, ok := network.BackendHostname(server.ID, server.EnvironmentID, backend); ok {
			mode = "hostname"
		}
		profile.Backends = append(profile.Backends, NetworkBackend{BackendID: backend.ID, AddressMode: mode})
	}
	return profile
}

// CheckNetwork resolves a configured server's network profile and refuses it
// when any required capability is not advertised by an adapter registered in
// the server's environment. Planning must call it before accepting work.
func (r *Runtime) CheckNetwork(serverID string) error {
	server, ok := r.servers[serverID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoNetworkProfile, serverID)
	}
	missing := MissingCapabilities(ProfileFromConfig(server), server.EnvironmentID, r.registeredAdapter)
	if len(missing) > 0 {
		return &NetworkError{ServerID: serverID, Missing: missing}
	}
	return nil
}

// NetworkProblems returns the configured servers that cannot be planned
// because of their network profile, keyed by server ID.
func (r *Runtime) NetworkProblems() map[string]error {
	problems := map[string]error{}
	for id := range r.servers {
		if err := r.CheckNetwork(id); err != nil {
			problems[id] = err
		}
	}
	return problems
}

func (r *Runtime) registeredAdapter(adapterID string) (RegisteredAdapter, bool) {
	if adapterID == "" || r.Registry == nil {
		return RegisteredAdapter{}, false
	}
	descriptor, err := r.Registry.Descriptor(adapterID)
	if err != nil {
		return RegisteredAdapter{}, false
	}
	capabilities := map[string]bool{}
	for _, capability := range descriptor.Capabilities {
		capabilities[capability.Name] = true
	}
	return RegisteredAdapter{EnvironmentID: r.adapterEnvironments[adapterID], Capabilities: capabilities}, true
}
