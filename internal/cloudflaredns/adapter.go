// Package cloudflaredns implements the controller's Cloudflare DNS
// integration adapter for the integration.dns.record.* capabilities.
//
// It manages only DNS-only (not proxied) A and AAAA records that are
// explicitly configured as managed, enforces the protocol's purpose rules
// (a direct-join record changes only with a current readiness assertion for
// the exact instance), and confirms every change by reading the record back.
package cloudflaredns

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/credential"
)

const (
	contractVersion       = "integration.dns.v1-draft"
	implementationVersion = "0.1.0"
	capabilityVersion     = "1"
	zoneResourceKind      = "dns-zone"
	minimumTTLSeconds     = 60
	defaultRequestTimeout = 30 * time.Second

	permissionInspect = "dns.record.inspect"
	permissionManage  = "dns.record.manage"
)

var (
	adapterIDPattern = regexp.MustCompile(`^integration\.[a-z0-9][a-z0-9.-]{0,147}$`)
	zoneIDPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hostnamePattern  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Purpose is the declared use of a DNS record upsert.
type Purpose string

const (
	PurposeBackendAddress Purpose = "backend_address"
	PurposeDirectJoin     Purpose = "direct_join"
	PurposeFallback       Purpose = "fallback"
)

// ResourceRef is the protocol's external resource reference.
type ResourceRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

// ManagedRecord lists what the controller may do with one record name.
type ManagedRecord struct {
	// Purposes the controller may use for upserts to this name.
	Purposes []Purpose
	// Fallback holds the operator-configured fallback values (for example the
	// lobby's address) per record type, "A" or "AAAA". Required when
	// Purposes includes PurposeFallback.
	Fallback map[string][]string
}

// Config contains the non-secret settings for one Cloudflare zone in one
// environment.
type Config struct {
	AdapterID     string
	EnvironmentID string
	// ZoneID is the Cloudflare zone ID; it is the zone_ref external_id.
	ZoneID string
	// ZoneName is the zone's DNS name, such as example.com. Managed records
	// must be in this zone.
	ZoneName string
	// APITokenRef is the systemd credential holding a Cloudflare API token
	// scoped to DNS edit for this zone.
	APITokenRef string
	// Records maps each managed record name to its allowed uses. Any other
	// name in the zone is refused.
	Records map[string]ManagedRecord
}

// CredentialResolver resolves a systemd credential reference.
type CredentialResolver interface {
	WithCredential(reference string, use func([]byte) error) error
}

// Adapter implements adapter.Implementation for one Cloudflare zone.
type Adapter struct {
	config          Config
	zoneRef         ResourceRef
	records         map[string]ManagedRecord
	protocolVersion string
	credentials     CredentialResolver
	client          *client
	now             func() time.Time
}

// New constructs the adapter. protocolVersion must match the loaded protocol
// registry. httpClient may be nil.
func New(config Config, protocolVersion string, credentials CredentialResolver, httpClient *http.Client) (*Adapter, error) {
	return newAdapter(config, protocolVersion, credentials, httpClient, defaultAPIBase, time.Now)
}

func newAdapter(config Config, protocolVersion string, credentials CredentialResolver, httpClient *http.Client, apiBase string, now func() time.Time) (*Adapter, error) {
	if !adapterIDPattern.MatchString(config.AdapterID) || config.EnvironmentID == "" || len(config.EnvironmentID) > 200 {
		return nil, errors.New("an integration adapter ID and environment are required")
	}
	if !zoneIDPattern.MatchString(config.ZoneID) || !validHostname(config.ZoneName) {
		return nil, errors.New("a Cloudflare zone ID and zone name are required")
	}
	if !strings.HasPrefix(config.APITokenRef, credential.SystemdReferenceScheme) || credentials == nil {
		return nil, errors.New("the Cloudflare API token must be a systemd credential reference")
	}
	if protocolVersion == "" || now == nil {
		return nil, errors.New("protocol version is required")
	}
	if len(config.Records) == 0 {
		return nil, errors.New("at least one managed record is required")
	}
	records := make(map[string]ManagedRecord, len(config.Records))
	for name, record := range config.Records {
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		if !validHostname(name) || (name != config.ZoneName && !strings.HasSuffix(name, "."+config.ZoneName)) {
			return nil, errors.New("managed records must be valid names inside the zone")
		}
		if len(record.Purposes) == 0 {
			return nil, errors.New("each managed record needs at least one purpose")
		}
		normalized := ManagedRecord{Purposes: append([]Purpose(nil), record.Purposes...), Fallback: map[string][]string{}}
		for _, purpose := range record.Purposes {
			switch purpose {
			case PurposeBackendAddress, PurposeDirectJoin:
			case PurposeFallback:
				if len(record.Fallback) == 0 {
					return nil, errors.New("fallback records need configured fallback values")
				}
			default:
				return nil, errors.New("record purpose must be backend_address, direct_join, or fallback")
			}
		}
		for recordType, values := range record.Fallback {
			canonical, ok := canonicalValues(recordType, values)
			if !ok || len(canonical) == 0 {
				return nil, errors.New("fallback values must be IP addresses matching the record type")
			}
			normalized.Fallback[recordType] = canonical
		}
		records[name] = normalized
	}
	base, err := url.Parse(apiBase)
	if err != nil || base.Scheme != "https" && !(base.Scheme == "http" && base.Hostname() == "127.0.0.1") {
		return nil, errors.New("Cloudflare API base must be HTTPS")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	httpCopy := *httpClient
	if httpCopy.Timeout <= 0 {
		httpCopy.Timeout = defaultRequestTimeout
	}
	httpCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Adapter{
		config:          config,
		zoneRef:         ResourceRef{AdapterID: config.AdapterID, EnvironmentID: config.EnvironmentID, ResourceKind: zoneResourceKind, ExternalID: config.ZoneID},
		records:         records,
		protocolVersion: protocolVersion,
		credentials:     credentials,
		client:          &client{base: strings.TrimSuffix(apiBase, "/"), zoneID: config.ZoneID, http: &httpCopy},
		now:             now,
	}, nil
}

// ZoneRef returns the zone reference callers put in record targets.
func (a *Adapter) ZoneRef() ResourceRef { return a.zoneRef }

// Descriptor advertises the DNS record capabilities.
func (a *Adapter) Descriptor() adapter.Descriptor {
	capability := func(name, schema string, mutating bool) adapter.Capability {
		permission := permissionInspect
		if mutating {
			permission = permissionManage
		}
		return adapter.Capability{
			Name: name, Version: capabilityVersion, Mutating: mutating,
			InputSchemaURI:      "urn:gameserver-controller:" + a.protocolVersion + ":integrations:" + schema + "-input",
			OutputSchemaURI:     "urn:gameserver-controller:" + a.protocolVersion + ":integrations:" + schema + "-output",
			RequiredPermissions: []string{permission},
		}
	}
	return adapter.Descriptor{
		AdapterID: a.config.AdapterID, Category: adapter.CategoryIntegration, DisplayName: "Cloudflare DNS integration",
		ImplementationVersion: implementationVersion, ProtocolVersions: []string{a.protocolVersion},
		ContractVersions:    []string{contractVersion},
		RequiredPermissions: []string{permissionInspect, permissionManage},
		Capabilities: []adapter.Capability{
			capability("integration.dns.record.delete", "dns-record-delete", true),
			capability("integration.dns.record.inspect", "dns-record-inspect", false),
			capability("integration.dns.record.upsert", "dns-record-upsert", true),
		},
		Constraints: map[string]any{
			"dns_provider": "Cloudflare", "record_types": []any{"A", "AAAA"}, "proxied_records": false,
			"minimum_ttl_seconds": minimumTTLSeconds, "managed_records_only": true,
		},
	}
}

// Invoke dispatches one schema-validated capability call.
func (a *Adapter) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if ctx == nil || a == nil {
		return adapter.InvokeResponse{}, errors.New("Cloudflare adapter and invocation context are required")
	}
	if !authorizedToken(request.Params.RequestContext.SecretRefs, a.config.APITokenRef) {
		return adapter.InvokeResponse{}, errors.New("the Cloudflare API token reference is not authorized for this invocation")
	}
	var (
		output  any
		outcome adapter.Outcome
		err     error
	)
	switch request.Params.Capability {
	case "integration.dns.record.inspect":
		output, outcome, err = a.inspect(ctx, request.Params.Input)
	case "integration.dns.record.upsert":
		output, outcome, err = a.upsert(ctx, request.Params.Input)
	case "integration.dns.record.delete":
		output, outcome, err = a.remove(ctx, request.Params.Input)
	default:
		return adapter.InvokeResponse{}, errors.New("unsupported Cloudflare DNS capability")
	}
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return adapter.InvokeResponse{}, errors.New("could not encode Cloudflare DNS result")
	}
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{
		Outcome: outcome, Output: encoded, ObservedAt: a.now().UTC(),
	}}, nil
}

func authorizedToken(refs []adapter.SecretReference, reference string) bool {
	for _, ref := range refs {
		if ref.SecretRef == reference {
			return true
		}
	}
	return false
}

func validHostname(value string) bool {
	return len(value) <= 253 && hostnamePattern.MatchString(value) && strings.Trim(value, "0123456789.") != ""
}

// canonicalValues parses IP values for a record type and returns them in
// canonical, sorted form.
func canonicalValues(recordType string, values []string) ([]string, bool) {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		ip, err := netip.ParseAddr(value)
		if err != nil || ip.Zone() != "" || (recordType == "A") != ip.Is4() || (recordType != "A" && recordType != "AAAA") {
			return nil, false
		}
		text := ip.String()
		if seen[text] {
			return nil, false
		}
		seen[text] = true
		result = append(result, text)
	}
	sort.Strings(result)
	return result, true
}

func sameValues(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
