// Package vultrhosting is the read-only Vultr hosting adapter. It inspects
// exact instances and finds controller-created instances by resource key
// through the Vultr v2 API. It never creates, changes, or destroys provider
// resources; those capabilities need the durable workflows and approvals the
// hosting contract requires and are not implemented here.
package vultrhosting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

const (
	contractVersion       = "hosting.v1-draft"
	implementationVersion = "0.1.0"
	capabilityVersion     = "1"
	permissionInspect     = "hosting.instance.inspect"
	resourceKind          = "instance"

	capabilityInspect = "hosting.instance.inspect"
	capabilityFind    = "hosting.instance.find_by_resource_key"
)

// Config binds one adapter profile to its environment and API token.
type Config struct {
	AdapterID     string
	EnvironmentID string
	// APITokenRef is the systemd credential holding a Vultr API token.
	APITokenRef string
}

// CredentialResolver lends a credential's bytes to use and clears them after.
type CredentialResolver interface {
	WithCredential(reference string, use func([]byte) error) error
}

// instanceReader is the read-only part of the Vultr client this adapter uses.
type instanceReader interface {
	GetInstance(ctx context.Context, token []byte, instanceID string) (vultrapi.Instance, error)
	ListInstances(ctx context.Context, token []byte) ([]vultrapi.Instance, error)
}

// Adapter implements adapter.Implementation for Vultr hosting reads.
type Adapter struct {
	config          Config
	protocolVersion string
	credentials     CredentialResolver
	client          instanceReader
	now             func() time.Time
}

// New builds an adapter for the production Vultr API. httpClient is optional.
func New(config Config, protocolVersion string, credentials CredentialResolver, httpClient *http.Client) (*Adapter, error) {
	return newAdapter(config, protocolVersion, credentials, vultrapi.NewClient(httpClient))
}

func newAdapter(config Config, protocolVersion string, credentials CredentialResolver, client instanceReader) (*Adapter, error) {
	if config.AdapterID == "" || config.EnvironmentID == "" || config.APITokenRef == "" {
		return nil, errors.New("Vultr hosting adapter needs an adapter ID, environment ID, and API token reference")
	}
	if protocolVersion == "" || credentials == nil {
		return nil, errors.New("Vultr hosting adapter needs a protocol version and a credential resolver")
	}
	return &Adapter{config: config, protocolVersion: protocolVersion, credentials: credentials, client: client, now: time.Now}, nil
}

// Descriptor reports the two read-only capabilities.
func (a *Adapter) Descriptor() adapter.Descriptor {
	capability := func(name, schema string) adapter.Capability {
		return adapter.Capability{
			Name: name, Version: capabilityVersion, Mutating: false,
			InputSchemaURI:      "urn:gameserver-controller:" + a.protocolVersion + ":hosting:" + schema + "-input",
			OutputSchemaURI:     "urn:gameserver-controller:" + a.protocolVersion + ":hosting:" + schema + "-output",
			RequiredPermissions: []string{permissionInspect},
		}
	}
	return adapter.Descriptor{
		AdapterID: a.config.AdapterID, Category: adapter.CategoryHosting, DisplayName: "Vultr hosting (read-only)",
		ImplementationVersion: implementationVersion, ProtocolVersions: []string{a.protocolVersion},
		ContractVersions: []string{contractVersion}, RequiredPermissions: []string{permissionInspect},
		Capabilities: []adapter.Capability{
			capability(capabilityFind, "instance-find-by-resource-key"),
			capability(capabilityInspect, "instance-inspect"),
		},
		Constraints: map[string]any{"hosting_provider": "Vultr", "read_only": true, "maximum_resource_key_matches": maxMatches},
	}
}

// Invoke runs one read-only capability with the profile's API token.
func (a *Adapter) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if ctx == nil || a == nil {
		return adapter.InvokeResponse{}, errors.New("Vultr hosting adapter and invocation context are required")
	}
	if !authorizedToken(request.Params.RequestContext.SecretRefs, a.config.APITokenRef) {
		return adapter.InvokeResponse{}, errors.New("the Vultr API token reference is not authorized for this invocation")
	}
	handler, ok := map[string]func(context.Context, json.RawMessage) (any, error){
		capabilityInspect: a.inspect,
		capabilityFind:    a.findByResourceKey,
	}[request.Params.Capability]
	if !ok {
		return adapter.InvokeResponse{}, errors.New("unsupported Vultr hosting capability")
	}
	output, err := handler(ctx, request.Params.Input)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return adapter.InvokeResponse{}, errors.New("could not encode Vultr hosting result")
	}
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{
		Outcome: adapter.OutcomeCompleted, Output: encoded, ObservedAt: a.now().UTC(),
	}}, nil
}

// withToken lends the API token to read for the duration of one call.
func (a *Adapter) withToken(read func(token []byte) error) error {
	return a.credentials.WithCredential(a.config.APITokenRef, read)
}

func authorizedToken(refs []adapter.SecretReference, reference string) bool {
	for _, ref := range refs {
		if ref.SecretRef == reference {
			return true
		}
	}
	return false
}

// decodeStrict rejects unknown fields and trailing data, as the input
// schemas do.
func decodeStrict(input json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data after input")
	}
	return nil
}
