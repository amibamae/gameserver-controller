// Package notification holds the operator notifier adapters: one sender per
// destination type (webhook.go, smtp.go, ntfy.go) behind one shared adapter
// that implements integration.notification.send.
//
// A notifier receives only notification-event fields. It never receives or
// sends player data, and its errors never include destination URLs,
// addresses, or credentials.
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
)

const (
	implementationVersion = "1.0.0"
	contractVersion       = "1"
	permissionSend        = "integration.notification.send"
)

// Event is the notification-event the adapter delivers.
type Event = integrationmonitor.Event

// CredentialResolver reads one systemd credential for the duration of use.
type CredentialResolver interface {
	WithCredential(reference string, use func([]byte) error) error
}

// Sender delivers one event to a destination using its secret. It returns
// integrationmonitor.Delivered, Rejected, or Unknown.
type Sender interface {
	// Kind names the destination type for the descriptor: webhook, email,
	// or push.
	Kind() string
	// DisplayName is the descriptor's display name.
	DisplayName() string
	Send(ctx context.Context, event Event, secret []byte) string
}

// Config is one notifier adapter.
type Config struct {
	AdapterID     string
	EnvironmentID string
	// SecretRef is the systemd credential reference the sender needs.
	SecretRef   string
	Sender      Sender
	Credentials CredentialResolver
}

// Adapter implements adapter.Implementation for one notifier.
type Adapter struct {
	config          Config
	protocolVersion string
	now             func() time.Time
}

// New returns a notifier adapter.
func New(config Config, protocolVersion string) (*Adapter, error) {
	if config.AdapterID == "" || config.EnvironmentID == "" || config.SecretRef == "" || config.Sender == nil || config.Credentials == nil {
		return nil, errors.New("notifier adapter ID, environment, secret reference, sender, and credentials are required")
	}
	return &Adapter{config: config, protocolVersion: protocolVersion, now: time.Now}, nil
}

// Descriptor advertises integration.notification.send. It is not mutating:
// a notification changes no managed resource, runs outside durable
// operations, and is delivered at least once (integrations section 7).
func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		AdapterID: a.config.AdapterID, Category: adapter.CategoryIntegration, DisplayName: a.config.Sender.DisplayName(),
		ImplementationVersion: implementationVersion, ProtocolVersions: []string{a.protocolVersion},
		ContractVersions: []string{contractVersion}, RequiredPermissions: []string{permissionSend},
		Capabilities: []adapter.Capability{{
			Name: integrationmonitor.SendCapability, Version: "1", Mutating: false,
			InputSchemaURI:      "urn:gameserver-controller:" + a.protocolVersion + ":integrations:notification-send-input",
			OutputSchemaURI:     "urn:gameserver-controller:" + a.protocolVersion + ":integrations:notification-send-output",
			RequiredPermissions: []string{permissionSend},
		}},
		Constraints: map[string]any{"delivery": "at_least_once", "destination_kind": a.config.Sender.Kind(), "player_data": "none"},
	}
}

// Invoke delivers one event. The registry has already validated the input
// against notification-send-input.
func (a *Adapter) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if ctx == nil || a == nil {
		return adapter.InvokeResponse{}, errors.New("notifier adapter and invocation context are required")
	}
	if request.Params.Capability != integrationmonitor.SendCapability {
		return adapter.InvokeResponse{}, errors.New("unsupported notifier capability")
	}
	if request.Params.RequestContext.EnvironmentID != a.config.EnvironmentID {
		return adapter.InvokeResponse{}, errors.New("the invocation environment is not this notifier's environment")
	}
	if !authorized(request.Params.RequestContext.SecretRefs, a.config.SecretRef) {
		return adapter.InvokeResponse{}, errors.New("the notifier credential reference is not authorized for this invocation")
	}
	var input struct {
		Event Event `json:"event"`
	}
	if err := json.Unmarshal(request.Params.Input, &input); err != nil {
		return adapter.InvokeResponse{}, errors.New("could not decode the notification event")
	}
	if input.Event.EnvironmentID != a.config.EnvironmentID {
		return adapter.InvokeResponse{}, errors.New("the event is for another environment")
	}
	outcome := integrationmonitor.Unknown
	err := a.config.Credentials.WithCredential(a.config.SecretRef, func(secret []byte) error {
		outcome = a.config.Sender.Send(ctx, input.Event, secret)
		return nil
	})
	if err != nil {
		// The credential could not be read, so nothing was sent.
		outcome = integrationmonitor.Rejected
	}
	encoded, err := json.Marshal(map[string]any{"event_id": input.Event.EventID, "outcome": outcome, "observed_at": a.now().UTC()})
	if err != nil {
		return adapter.InvokeResponse{}, errors.New("could not encode the notification result")
	}
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{
		Outcome: adapter.OutcomeCompleted, Output: encoded, ObservedAt: a.now().UTC(),
	}}, nil
}

func authorized(refs []adapter.SecretReference, reference string) bool {
	for _, ref := range refs {
		if ref.SecretRef == reference {
			return true
		}
	}
	return false
}

// classifyHTTP maps an HTTP status to a delivery outcome: 2xx delivered;
// 408, 425, 429 and 5xx unknown (worth retrying); other 4xx rejected.
func classifyHTTP(status int) string {
	switch {
	case status >= 200 && status < 300:
		return integrationmonitor.Delivered
	case status == 408 || status == 425 || status == 429 || status >= 500:
		return integrationmonitor.Unknown
	default:
		return integrationmonitor.Rejected
	}
}
