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

// validateServerNetwork checks a server's network profile and that every
// integration adapter it requires is configured.
func validateServerNetwork(problems *[]string, label string, server ServerProfile, adapters map[string]AdapterProfile) {
	if server.Network == nil {
		return
	}
	check := networkCheck{problems: problems, label: label + ".network", server: server, network: *server.Network, adapters: adapters}
	check.template()
	if !check.joinMode() {
		return
	}
	check.integrationUse()
	check.adapterEnvironments()
	check.zoneMembership()
	check.backends()
}

// networkCheck collects one server's network problems under its label.
type networkCheck struct {
	problems *[]string
	label    string
	server   ServerProfile
	network  ServerNetwork
	adapters map[string]AdapterProfile
}

// add records label+problem unless ok.
func (c networkCheck) add(ok bool, problem string) {
	if !ok {
		*c.problems = append(*c.problems, c.label+problem)
	}
}

// usesHostname reports whether any backend is reached by a managed name.
func (c networkCheck) usesHostname() bool {
	for _, backend := range c.network.Backends {
		if backend.effectiveAddressMode(c.network) == "hostname" {
			return true
		}
	}
	return false
}

func (c networkCheck) template() {
	template := c.network.BackendHostnameTemplate
	if template == "" {
		return
	}
	placeholdersOK := strings.Contains(template, "{backend}")
	for _, placeholder := range templatePlaceholder.FindAllString(template, -1) {
		placeholdersOK = placeholdersOK && (placeholder == "{backend}" || placeholder == "{server}" || placeholder == "{environment}")
	}
	c.add(placeholdersOK, ".backend_hostname_template must contain {backend} and may only use {backend}, {server}, and {environment}")
	c.add(c.network.JoinMode == "proxy", ".backend_hostname_template is only used with join_mode proxy")
}

// joinMode checks the fields each join mode needs or forbids. It reports
// false when the remaining checks do not apply (join mode none or unknown).
func (c networkCheck) joinMode() bool {
	network := c.network
	switch network.JoinMode {
	case "proxy":
		checkServerAdapter(c.problems, c.label+".proxy", network.Proxy, "integration", c.adapters, true)
		c.add(len(network.Backends) > 0, ".backends must list at least one backend for join_mode proxy")
		c.add(network.JoinHostname == "", ".join_hostname is only used with join_mode direct_dns")
		if legacy := c.server.Adapters.Integration; legacy != "" {
			c.add(legacy == network.Proxy, ".proxy must match adapters.integration when both are set")
		}
		return true
	case "direct_dns":
		c.add(network.Proxy == "", ".proxy is only used with join_mode proxy")
		c.add(len(network.Backends) == 0, ".backends are only used with join_mode proxy")
		c.add(validDNSHostname(network.JoinHostname), ".join_hostname must be a lower-case DNS hostname")
		validateFallbackRecords(c.problems, c.label, network.FallbackRecords)
		return true
	case "none":
		c.add(network.Proxy == "" && network.DNS == "" && network.JoinHostname == "" && len(network.Backends) == 0,
			" with join_mode none must not set proxy, dns, join_hostname, or backends")
	default:
		c.add(false, ".join_mode must be proxy, direct_dns, or none")
	}
	return false
}

// integrationUse checks the DNS adapter and fallback records against what
// the join mode and backends use.
func (c networkCheck) integrationUse() {
	network := c.network
	usesHostname := c.usesHostname()
	checkServerAdapter(c.problems, c.label+".dns", network.DNS, "integration", c.adapters, network.JoinMode == "direct_dns" || usesHostname)
	if network.JoinMode == "proxy" && !usesHostname {
		c.add(network.DNS == "", ".dns is only used with join_mode direct_dns or hostname backends")
	}
	if network.JoinMode != "direct_dns" {
		c.add(len(network.FallbackRecords) == 0, ".fallback_records are only used with join_mode direct_dns")
	}
}

// adapterEnvironments checks that built-in integration adapters, which are
// bound to one environment, are the server's: a server must not route
// through another environment's proxy or DNS zone.
func (c networkCheck) adapterEnvironments() {
	for field, id := range map[string]string{".proxy": c.network.Proxy, ".dns": c.network.DNS} {
		if environment := BoundEnvironment(c.adapters[id]); id != "" && environment != "" {
			c.add(environment == c.server.EnvironmentID, field+" adapter "+id+" belongs to environment "+environment+", not "+c.server.EnvironmentID)
		}
	}
}

// zoneMembership checks that every managed name lies in the DNS adapter's
// zone, when the adapter owns one.
func (c networkCheck) zoneMembership() {
	owner, ok := c.adapters[c.network.DNS].Settings.(DNSZoneOwner)
	if !ok {
		return
	}
	zone := owner.DNSZone()
	names := []string{}
	if c.network.JoinMode == "direct_dns" {
		names = append(names, c.network.JoinHostname)
	}
	for _, backend := range c.network.Backends {
		if name, ok := c.network.BackendHostname(c.server.ID, c.server.EnvironmentID, backend); ok && name != "" {
			names = append(names, name)
		}
	}
	for _, name := range names {
		c.add(inZone(name, zone), " hostname "+name+" is outside the DNS zone "+zone)
	}
}

func (c networkCheck) backends() {
	seen := make(map[string]bool, len(c.network.Backends))
	for i, backend := range c.network.Backends {
		entry := fmt.Sprintf(".backends[%d]", i)
		c.add(backendIDPattern.MatchString(backend.ID), entry+".id must be a proxy server name")
		c.add(!seen[backend.ID], entry+".id is a duplicate")
		seen[backend.ID] = true
		c.add(backend.Port >= 1 && backend.Port <= 65535, entry+".port must be between 1 and 65535")
		c.backendAddress(entry, backend)
	}
}

// backendAddress checks one backend's address fields for its mode.
func (c networkCheck) backendAddress(entry string, backend NetworkBackend) {
	switch backend.effectiveAddressMode(c.network) {
	case "instance_ip":
		c.add(backend.Hostname == "", entry+".hostname is only used with address_mode hostname")
	case "hostname":
		if c.network.BackendHostnameTemplate == "" {
			c.add(validDNSHostname(backend.Hostname), entry+".hostname must be a lower-case DNS hostname")
			return
		}
		c.add(backend.Hostname == "", entry+".hostname must not be set when backend_hostname_template is set")
		name, _ := c.network.BackendHostname(c.server.ID, c.server.EnvironmentID, backend)
		c.add(validDNSHostname(name), entry+" hostname from backend_hostname_template is not a valid lower-case DNS hostname")
	default:
		c.add(false, entry+".address_mode must be instance_ip or hostname")
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
