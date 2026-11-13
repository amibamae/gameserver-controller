package config

import (
	"reflect"
	"testing"
)

// zoneSettings is integration settings bound to one environment and, when
// zone is set, owning a DNS zone.
type zoneSettings struct{ environment, zone string }

func (zoneSettings) Validate(SettingsCheck)     {}
func (s zoneSettings) BoundEnvironment() string { return s.environment }

type dnsZoneSettings struct{ zoneSettings }

func (s dnsZoneSettings) DNSZone() string { return s.zone }

func networkAdapters() map[string]AdapterProfile {
	return map[string]AdapterProfile{
		"proxy.velocity": {ID: "proxy.velocity", Category: "integration", Settings: zoneSettings{environment: "staging"}},
		"proxy.other":    {ID: "proxy.other", Category: "integration", Settings: zoneSettings{environment: "production"}},
		"dns.cloudflare": {ID: "dns.cloudflare", Category: "integration", Settings: dnsZoneSettings{zoneSettings{environment: "staging", zone: "mc.example.com"}}},
		"hosting.vultr":  {ID: "hosting.vultr", Category: "hosting"},
		"dns.unbound":    {ID: "dns.unbound", Category: "integration"},
	}
}

// networkCases returns server network profiles that each break some rules.
func networkCases() map[string]*ServerNetwork {
	backend := NetworkBackend{ID: "paper-main", AddressMode: "instance_ip", Port: 25565}
	return map[string]*ServerNetwork{
		"absent":         nil,
		"valid proxy":    {JoinMode: "proxy", Proxy: "proxy.velocity", Backends: []NetworkBackend{backend}},
		"unknown mode":   {JoinMode: "bridge"},
		"none with data": {JoinMode: "none", Proxy: "proxy.velocity", Backends: []NetworkBackend{backend}},
		"proxy missing everything": {JoinMode: "proxy", JoinHostname: "play.mc.example.com", DNS: "dns.cloudflare",
			FallbackRecords: map[string][]string{"A": {"203.0.113.1"}}},
		"proxy wrong adapters":    {JoinMode: "proxy", Proxy: "hosting.vultr", DNS: "missing.dns", Backends: []NetworkBackend{backend}},
		"proxy other environment": {JoinMode: "proxy", Proxy: "proxy.other", Backends: []NetworkBackend{backend}},
		"template rules": {JoinMode: "direct_dns", BackendHostnameTemplate: "{backend}.{zone}", JoinHostname: "play.mc.example.com", DNS: "dns.cloudflare",
			FallbackRecords: map[string][]string{"A": {"203.0.113.1"}}},
		"template backends": {JoinMode: "proxy", Proxy: "proxy.velocity", DNS: "dns.cloudflare", BackendHostnameTemplate: "{backend}.{environment}.mc.example.com",
			Backends: []NetworkBackend{{ID: "paper-main", Port: 25565}, {ID: "Bad_ID", Port: 25565, Hostname: "x.mc.example.com"}, {ID: "paper-main", Port: 0}}},
		"hostname backends outside zone": {JoinMode: "proxy", Proxy: "proxy.velocity", DNS: "dns.cloudflare",
			Backends: []NetworkBackend{{ID: "a", AddressMode: "hostname", Hostname: "a.other.example.com", Port: 1}, {ID: "b", AddressMode: "hostname", Hostname: "Not A Host", Port: 65535}}},
		"hostname backend without dns": {JoinMode: "proxy", Proxy: "proxy.velocity",
			Backends: []NetworkBackend{{ID: "a", AddressMode: "hostname", Hostname: "a.mc.example.com", Port: 25565}}},
		"instance backend with hostname and bad mode": {JoinMode: "proxy", Proxy: "proxy.velocity", DNS: "dns.cloudflare",
			Backends: []NetworkBackend{{ID: "a", AddressMode: "instance_ip", Hostname: "a.mc.example.com", Port: 70000}, {ID: "b", AddressMode: "magic", Port: 1}}},
		"direct dns valid":        {JoinMode: "direct_dns", DNS: "dns.cloudflare", JoinHostname: "play.mc.example.com", FallbackRecords: map[string][]string{"A": {"203.0.113.1"}}},
		"direct dns outside zone": {JoinMode: "direct_dns", DNS: "dns.cloudflare", JoinHostname: "play.other.example.com", FallbackRecords: map[string][]string{"A": {"203.0.113.1"}}},
		"direct dns misused fields": {JoinMode: "direct_dns", Proxy: "proxy.velocity", Backends: []NetworkBackend{backend}, JoinHostname: "Bad Host",
			FallbackRecords: map[string][]string{"A": {"203.0.113.1"}}},
		"direct dns no records": {JoinMode: "direct_dns", DNS: "dns.unbound", JoinHostname: "play.mc.example.com"},
	}
}

// TestValidateServerNetworkReportsEveryRuleInOrder pins the exact problem
// list, in order, for each network profile above.
func TestValidateServerNetworkReportsEveryRuleInOrder(t *testing.T) {
	want := map[string][]string{
		"absent":                                      []string(nil),
		"direct dns misused fields":                   []string{"servers[0].network.proxy is only used with join_mode proxy", "servers[0].network.backends are only used with join_mode proxy", "servers[0].network.join_hostname must be a lower-case DNS hostname", "servers[0].network.dns is required"},
		"direct dns no records":                       []string{"servers[0].network.fallback_records is required for join_mode direct_dns"},
		"direct dns outside zone":                     []string{"servers[0].network hostname play.other.example.com is outside the DNS zone mc.example.com"},
		"direct dns valid":                            []string(nil),
		"hostname backend without dns":                []string{"servers[0].network.dns is required"},
		"hostname backends outside zone":              []string{"servers[0].network hostname a.other.example.com is outside the DNS zone mc.example.com", "servers[0].network hostname Not A Host is outside the DNS zone mc.example.com", "servers[0].network.backends[1].hostname must be a lower-case DNS hostname"},
		"instance backend with hostname and bad mode": []string{"servers[0].network.dns is only used with join_mode direct_dns or hostname backends", "servers[0].network.backends[0].port must be between 1 and 65535", "servers[0].network.backends[0].hostname is only used with address_mode hostname", "servers[0].network.backends[1].address_mode must be instance_ip or hostname"},
		"none with data":                              []string{"servers[0].network with join_mode none must not set proxy, dns, join_hostname, or backends"},
		"proxy missing everything":                    []string{"servers[0].network.proxy is required", "servers[0].network.backends must list at least one backend for join_mode proxy", "servers[0].network.join_hostname is only used with join_mode direct_dns", "servers[0].network.dns is only used with join_mode direct_dns or hostname backends", "servers[0].network.fallback_records are only used with join_mode direct_dns"},
		"proxy other environment":                     []string{"servers[0].network.proxy adapter proxy.other belongs to environment production, not staging"},
		"proxy wrong adapters":                        []string{"servers[0].network.proxy must reference a integration adapter profile", "servers[0].network.proxy must match adapters.integration when both are set", "servers[0].network.dns references unknown adapter missing.dns", "servers[0].network.dns is only used with join_mode direct_dns or hostname backends"},
		"template backends":                           []string{"servers[0].network.backends[1].id must be a proxy server name", "servers[0].network.backends[1].hostname must not be set when backend_hostname_template is set", "servers[0].network.backends[1] hostname from backend_hostname_template is not a valid lower-case DNS hostname", "servers[0].network.backends[2].id is a duplicate", "servers[0].network.backends[2].port must be between 1 and 65535"},
		"template rules":                              []string{"servers[0].network.backend_hostname_template must contain {backend} and may only use {backend}, {server}, and {environment}", "servers[0].network.backend_hostname_template is only used with join_mode proxy"},
		"unknown mode":                                []string{"servers[0].network.join_mode must be proxy, direct_dns, or none"},
		"valid proxy":                                 []string(nil),
	}
	cases := networkCases()
	if len(cases) != len(want) {
		t.Fatalf("%d cases but %d expectations", len(cases), len(want))
	}
	for name, network := range cases {
		t.Run(name, func(t *testing.T) {
			server := ServerProfile{ID: "minecraft", EnvironmentID: "staging", Network: network}
			if name == "proxy wrong adapters" {
				server.Adapters.Integration = "proxy.velocity"
			}
			var problems []string
			validateServerNetwork(&problems, "servers[0]", server, networkAdapters())
			if !reflect.DeepEqual(problems, want[name]) {
				t.Fatalf("problems =\n%q\nwant\n%q", problems, want[name])
			}
		})
	}
}
