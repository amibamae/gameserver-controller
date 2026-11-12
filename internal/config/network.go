package config

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// ServerNetwork is a server's network profile: how players reach it and
// which integration adapters that requires. See
// protocol/adapters/integrations.md section 6. A missing block means join
// mode "none".
type ServerNetwork struct {
	// JoinMode is "proxy", "direct_dns", or "none".
	JoinMode string `yaml:"join_mode"`
	// Proxy is the proxy integration adapter; required for join mode proxy.
	Proxy string `yaml:"proxy"`
	// DNS is the DNS integration adapter; required for join mode direct_dns
	// and for any backend that uses a managed hostname.
	DNS string `yaml:"dns"`
	// JoinHostname is the name players join through in direct_dns mode.
	JoinHostname string `yaml:"join_hostname"`
	// BackendHostnameTemplate gives every hostname backend its own DNS name
	// from one scheme, such as "{backend}.{environment}.mc.example.com".
	// Placeholders: {backend} (required), {server}, and {environment}.
	BackendHostnameTemplate string `yaml:"backend_hostname_template"`
	// Backends are the proxy backends for this server (proxy mode only).
	Backends []NetworkBackend `yaml:"backends"`
	// FallbackRecords are the values the join hostname points at while the
	// server is not ready, normally the lobby's address, keyed by record type
	// ("A" or "AAAA"). Required for join mode direct_dns.
	FallbackRecords map[string][]string `yaml:"fallback_records"`
}

// NetworkBackend says how the proxy reaches one backend.
type NetworkBackend struct {
	ID string `yaml:"id"`
	// AddressMode is "instance_ip" (the instance's current public IP) or
	// "hostname" (a managed DNS name kept pointed at the instance). It
	// defaults to "hostname" when the profile sets a hostname template.
	AddressMode string `yaml:"address_mode"`
	Hostname    string `yaml:"hostname"`
	Port        int    `yaml:"port"`
}

// inZone reports whether name is the zone apex or a name below it.
func inZone(name, zone string) bool {
	return name == zone || strings.HasSuffix(name, "."+zone)
}

var templatePlaceholder = regexp.MustCompile(`\{[^}]*\}`)

// BackendHostname returns the DNS name for a hostname-mode backend: its
// explicit hostname, or the profile's template filled in for this server.
func (n ServerNetwork) BackendHostname(serverID, environmentID string, backend NetworkBackend) (string, bool) {
	if backend.effectiveAddressMode(n) != "hostname" {
		return "", false
	}
	if n.BackendHostnameTemplate == "" {
		return backend.Hostname, backend.Hostname != ""
	}
	name := strings.NewReplacer("{backend}", backend.ID, "{server}", serverID, "{environment}", environmentID).Replace(n.BackendHostnameTemplate)
	return name, true
}

func (b NetworkBackend) effectiveAddressMode(n ServerNetwork) string {
	if b.AddressMode == "" && n.BackendHostnameTemplate != "" {
		return "hostname"
	}
	return b.AddressMode
}

func validDNSHostname(value string) bool {
	return len(value) <= 253 && dnsHostnamePattern.MatchString(value) && !numericHostPattern.MatchString(value)
}

// validateServerNetwork checks a server's network profile and that every
// integration adapter it requires is configured.
func validateServerNetwork(problems *[]string, label string, server ServerProfile, adapters map[string]AdapterProfile) {
	network := server.Network
	if network == nil {
		return
	}
	label += ".network"
	add := func(ok bool, problem string) {
		if !ok {
			*problems = append(*problems, label+problem)
		}
	}
	usesHostname := false
	for _, backend := range network.Backends {
		usesHostname = usesHostname || backend.effectiveAddressMode(*network) == "hostname"
	}
	if template := network.BackendHostnameTemplate; template != "" {
		placeholdersOK := strings.Contains(template, "{backend}")
		for _, placeholder := range templatePlaceholder.FindAllString(template, -1) {
			placeholdersOK = placeholdersOK && (placeholder == "{backend}" || placeholder == "{server}" || placeholder == "{environment}")
		}
		add(placeholdersOK, ".backend_hostname_template must contain {backend} and may only use {backend}, {server}, and {environment}")
		add(network.JoinMode == "proxy", ".backend_hostname_template is only used with join_mode proxy")
	}
	switch network.JoinMode {
	case "proxy":
		checkServerAdapter(problems, label+".proxy", network.Proxy, "integration", adapters, true)
		add(len(network.Backends) > 0, ".backends must list at least one backend for join_mode proxy")
		add(network.JoinHostname == "", ".join_hostname is only used with join_mode direct_dns")
		if legacy := server.Adapters.Integration; legacy != "" {
			add(legacy == network.Proxy, ".proxy must match adapters.integration when both are set")
		}
	case "direct_dns":
		add(network.Proxy == "", ".proxy is only used with join_mode proxy")
		add(len(network.Backends) == 0, ".backends are only used with join_mode proxy")
		add(validDNSHostname(network.JoinHostname), ".join_hostname must be a lower-case DNS hostname")
		validateFallbackRecords(problems, label, network.FallbackRecords)
	case "none":
		add(network.Proxy == "" && network.DNS == "" && network.JoinHostname == "" && len(network.Backends) == 0,
			" with join_mode none must not set proxy, dns, join_hostname, or backends")
		return
	default:
		add(false, ".join_mode must be proxy, direct_dns, or none")
		return
	}
	checkServerAdapter(problems, label+".dns", network.DNS, "integration", adapters, network.JoinMode == "direct_dns" || usesHostname)
	if network.JoinMode == "proxy" && !usesHostname {
		add(network.DNS == "", ".dns is only used with join_mode direct_dns or hostname backends")
	}
	if network.JoinMode != "direct_dns" {
		add(len(network.FallbackRecords) == 0, ".fallback_records are only used with join_mode direct_dns")
	}
	// Built-in integration adapters are bound to one environment; a server
	// must not route through another environment's proxy or DNS zone.
	for field, id := range map[string]string{".proxy": network.Proxy, ".dns": network.DNS} {
		if environment := BoundEnvironment(adapters[id]); id != "" && environment != "" {
			add(environment == server.EnvironmentID, field+" adapter "+id+" belongs to environment "+environment+", not "+server.EnvironmentID)
		}
	}
	if owner, ok := adapters[network.DNS].Settings.(DNSZoneOwner); ok {
		zone := owner.DNSZone()
		names := []string{}
		if network.JoinMode == "direct_dns" {
			names = append(names, network.JoinHostname)
		}
		for _, backend := range network.Backends {
			if name, ok := network.BackendHostname(server.ID, server.EnvironmentID, backend); ok && name != "" {
				names = append(names, name)
			}
		}
		for _, name := range names {
			add(inZone(name, zone), " hostname "+name+" is outside the DNS zone "+zone)
		}
	}
	seen := make(map[string]bool, len(network.Backends))
	for i, backend := range network.Backends {
		entry := fmt.Sprintf(".backends[%d]", i)
		add(backendIDPattern.MatchString(backend.ID), entry+".id must be a proxy server name")
		add(!seen[backend.ID], entry+".id is a duplicate")
		seen[backend.ID] = true
		add(backend.Port >= 1 && backend.Port <= 65535, entry+".port must be between 1 and 65535")
		switch backend.effectiveAddressMode(*network) {
		case "instance_ip":
			add(backend.Hostname == "", entry+".hostname is only used with address_mode hostname")
		case "hostname":
			if network.BackendHostnameTemplate != "" {
				add(backend.Hostname == "", entry+".hostname must not be set when backend_hostname_template is set")
				name, _ := network.BackendHostname(server.ID, server.EnvironmentID, backend)
				add(validDNSHostname(name), entry+" hostname from backend_hostname_template is not a valid lower-case DNS hostname")
			} else {
				add(validDNSHostname(backend.Hostname), entry+".hostname must be a lower-case DNS hostname")
			}
		default:
			add(false, entry+".address_mode must be instance_ip or hostname")
		}
	}
}

// validateFallbackRecords checks the direct_dns fallback values: at least one
// A (IPv4) or AAAA (IPv6) record, each a literal address.
func validateFallbackRecords(problems *[]string, label string, records map[string][]string) {
	if len(records) == 0 {
		*problems = append(*problems, label+".fallback_records is required for join_mode direct_dns")
		return
	}
	for recordType, values := range records {
		if recordType != "A" && recordType != "AAAA" {
			*problems = append(*problems, label+".fallback_records keys must be A or AAAA")
			continue
		}
		if len(values) == 0 || len(values) > 20 {
			*problems = append(*problems, label+".fallback_records."+recordType+" must list 1 to 20 addresses")
		}
		for _, value := range values {
			address, err := netip.ParseAddr(value)
			if err != nil || address.Zone() != "" || address.IsUnspecified() || (recordType == "A") != address.Is4() {
				*problems = append(*problems, label+".fallback_records."+recordType+" has an invalid address "+value)
			}
		}
	}
}

// validateProxyBackendIDs requires backend IDs to be unique among servers that
// share a proxy, because the proxy knows backends only by ID.
func validateProxyBackendIDs(problems *[]string, servers []ServerProfile) {
	owners := map[string]string{}
	for i, server := range servers {
		if server.Network == nil || server.Network.Proxy == "" {
			continue
		}
		for j, backend := range server.Network.Backends {
			key := server.Network.Proxy + "\x00" + backend.ID
			label := fmt.Sprintf("servers[%d].network.backends[%d]", i, j)
			if owner, taken := owners[key]; taken && owner != fmt.Sprintf("servers[%d]", i) {
				*problems = append(*problems, label+" id "+backend.ID+" is already used on proxy "+server.Network.Proxy+" by "+owner)
				continue
			}
			owners[key] = fmt.Sprintf("servers[%d]", i)
		}
	}
}

// validateUniqueBackendHostnames requires every backend hostname to belong to
// exactly one backend, so each backend server has its own domain.
func validateUniqueBackendHostnames(problems *[]string, servers []ServerProfile) {
	owners := map[string]string{}
	for i, server := range servers {
		if server.Network == nil {
			continue
		}
		for j, backend := range server.Network.Backends {
			name, ok := server.Network.BackendHostname(server.ID, server.EnvironmentID, backend)
			if !ok || name == "" {
				continue
			}
			label := fmt.Sprintf("servers[%d].network.backends[%d]", i, j)
			if owner, taken := owners[name]; taken {
				*problems = append(*problems, label+" hostname is already used by "+owner)
				continue
			}
			owners[name] = label
		}
	}
}
