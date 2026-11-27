package velocityproxy

import (
	"context"
	"time"
)

// ResourceRef is the protocol's external resource reference.
type ResourceRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

// BackendTarget names one proxy backend bound to one logical server,
// environment, and exact hosting instance.
type BackendTarget struct {
	ServerID      string      `json:"server_id"`
	EnvironmentID string      `json:"environment_id"`
	BackendID     string      `json:"backend_id"`
	InstanceRef   ResourceRef `json:"instance_ref"`
}

// ReadinessAssertion is the controller-supplied readiness evidence.
type ReadinessAssertion struct {
	AssertionID         string      `json:"assertion_id"`
	ServerID            string      `json:"server_id"`
	EnvironmentID       string      `json:"environment_id"`
	InstanceRef         ResourceRef `json:"instance_ref"`
	ReadinessCheckSetID string      `json:"readiness_check_set_id"`
	ObservedAt          time.Time   `json:"observed_at"`
	ExpiresAt           time.Time   `json:"expires_at"`
}

// RegistrationState is the proxy's view of one backend name.
type RegistrationState string

const (
	Registered   RegistrationState = "registered"
	Draining     RegistrationState = "draining"
	Unregistered RegistrationState = "unregistered"
	Unknown      RegistrationState = "unknown"
)

// BackendObservation is what the proxy reports for one backend name.
type BackendObservation struct {
	Registration RegistrationState
	// InstanceRef is the hosting instance the proxy currently routes this
	// backend name to, or nil when it is not bound to any known instance.
	InstanceRef *ResourceRef
	// Routable reports whether the proxy would send new connections to the
	// backend. It is ignored unless Registration is Registered.
	Routable bool
	// Address is the host:port the proxy connects to for this backend.
	Address string
	// ActiveSessions is the number of connected sessions, when known.
	ActiveSessions *int64
	ObservedAt     time.Time
}

// ConnectionState is the proxy's view of one session.
type ConnectionState string

const (
	Connected      ConnectionState = "connected"
	Disconnected   ConnectionState = "disconnected"
	SessionUnknown ConnectionState = "unknown"
)

// SessionObservation is what the proxy reports for one opaque session.
type SessionObservation struct {
	State ConnectionState
	// BackendID is the backend the session is connected to, when connected.
	BackendID string
	// WaitingCohortID is the waiting cohort the session belongs to, if any.
	WaitingCohortID string
	ObservedAt      time.Time
}

// Bridge is the transport to one running Velocity proxy instance. Methods
// must not return player names, account UUIDs, IP addresses, or chat, and
// their errors must not embed session identifiers.
type Bridge interface {
	InspectBackend(ctx context.Context, backendID string) (BackendObservation, error)
	// RegisterBackend binds the backend name to the instance and the
	// host:port address the proxy should connect to.
	RegisterBackend(ctx context.Context, target BackendTarget, address string) error
	DrainBackend(ctx context.Context, target BackendTarget) error
	UnregisterBackend(ctx context.Context, target BackendTarget) error
	InspectSession(ctx context.Context, sessionID string) (SessionObservation, error)
	// TransferSession moves one connected session to the destination backend
	// and returns nil only after the proxy confirms the connection.
	TransferSession(ctx context.Context, sessionID, destinationBackendID string) error
	// WaitingCohort lists every session currently in one waiting cohort.
	WaitingCohort(ctx context.Context, cohortID string) ([]string, time.Time, error)
}

// AddressResolver returns the current host:port address the proxy should use
// to reach a backend's instance. See ConfiguredAddresses.
type AddressResolver interface {
	ResolveBackendAddress(ctx context.Context, target BackendTarget) (string, error)
}
