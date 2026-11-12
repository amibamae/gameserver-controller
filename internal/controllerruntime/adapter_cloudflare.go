package controllerruntime

import (
	"errors"
	"sort"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/cloudflaredns"
	"github.com/amibamae/gameserver-controller/internal/config"
)

// Cloudflare DNS integration adapter (`implementation: cloudflare`).

const cloudflareAPITokenSecret = "api_token"

// CloudflareSettings is the `cloudflare:` block for one zone. The record names
// it may change are derived from the network profiles that use it.
type CloudflareSettings struct {
	EnvironmentID string `yaml:"environment_id"`
	ZoneID        string `yaml:"zone_id"`
	ZoneName      string `yaml:"zone_name"`
}

func (c *CloudflareSettings) BoundEnvironment() string { return c.EnvironmentID }

func (c *CloudflareSettings) DNSZone() string { return c.ZoneName }

func (c *CloudflareSettings) Validate(check config.SettingsCheck) {
	check.Require(check.KnownEnvironment(c.EnvironmentID), "environment_id must name a configured environment")
	check.Require(len(c.ZoneID) == 32 && strings.Trim(c.ZoneID, "0123456789abcdef") == "", "zone_id must be a 32-character lower-case hex Cloudflare zone ID")
	check.Require(config.ValidDNSHostname(c.ZoneName), "zone_name must be a lower-case DNS name")
}

func init() {
	registerAdapter(config.Implementation{
		Name: "cloudflare", Category: "integration", Secrets: []string{cloudflareAPITokenSecret},
		NewSettings: func() config.ImplementationSettings { return &CloudflareSettings{} },
	}, adapterBuilder{
		check: func(b adapterBuild) error {
			_, err := buildCloudflare(b.value, b.profile, b.protocolVersion, unavailableCredentials{})
			return err
		},
		build: func(b adapterBuild) (builtAdapter, error) {
			credentials, err := b.credentials()
			if err != nil {
				return builtAdapter{}, err
			}
			adapter, err := buildCloudflare(b.value, b.profile, b.protocolVersion, credentials)
			return builtAdapter{implementation: adapter}, err
		},
	})
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
	c, ok := config.SettingsOf[*CloudflareSettings](profile)
	if !ok {
		return nil, errors.New("cloudflare settings are missing")
	}
	records := cloudflareRecords(value, profile.ID)
	if len(records) == 0 {
		return nil, errors.New("no server network profile uses this DNS adapter, so it manages no records; remove it or reference it from a network profile")
	}
	return cloudflaredns.New(cloudflaredns.Config{
		AdapterID: profile.ID, EnvironmentID: c.EnvironmentID, ZoneID: c.ZoneID, ZoneName: c.ZoneName,
		APITokenRef: profile.SecretRefs[cloudflareAPITokenSecret].SecretRef, Records: records,
	}, protocolVersion, credentials, nil)
}
