package velocityproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/credential"
)

// The proxy bridge API is specified in docs/PROXY_BRIDGE_API.md.
const (
	bridgeAPIVersion     = "1"
	maxBridgeResponse    = 1 << 20
	defaultBridgeTimeout = 15 * time.Second
)

var (
	// Bridge errors use fixed messages: net/http errors include the URL,
	// which would leak session IDs into logs.
	errBridgeTransport = errors.New("proxy bridge request failed")
	errBridgeResponse  = errors.New("proxy bridge returned an invalid response")
	errBridgeRefused   = errors.New("proxy bridge refused the request")

	backendIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	cohortIDPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

// CredentialResolver resolves a systemd credential reference.
type CredentialResolver interface {
	WithCredential(reference string, use func([]byte) error) error
}

// HTTPBridgeConfig configures the client for one proxy bridge.
type HTTPBridgeConfig struct {
	// BaseURL is the bridge root, such as http://127.0.0.1:25580. Plain HTTP
	// is allowed only to a loopback address.
	BaseURL string
	// TokenRef is the systemd credential holding the bearer token.
	TokenRef string
	// ProxyInstanceID is the ID the bridge must report in /v1/info.
	ProxyInstanceID string
	// Timeout bounds each request; zero means 15 seconds.
	Timeout time.Duration
}

// HTTPBridge implements Bridge over the proxy bridge API.
type HTTPBridge struct {
	base        *url.URL
	tokenRef    string
	instanceID  string
	credentials CredentialResolver
	client      *http.Client
	now         func() time.Time
}

// NewHTTPBridge validates the configuration and returns a client. It does
// not contact the bridge; call CheckInfo to verify the connection.
func NewHTTPBridge(config HTTPBridgeConfig, credentials CredentialResolver, httpClient *http.Client) (*HTTPBridge, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Hostname() == "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("proxy bridge URL must be an absolute URL without user information, path, query, or fragment")
	}
	switch base.Scheme {
	case "https":
	case "http":
		ip := net.ParseIP(base.Hostname())
		if !(ip != nil && ip.IsLoopback()) && !strings.EqualFold(base.Hostname(), "localhost") {
			return nil, errors.New("plain HTTP to the proxy bridge is allowed only on loopback")
		}
	default:
		return nil, errors.New("proxy bridge URL must use http or https")
	}
	if !strings.HasPrefix(config.TokenRef, credential.SystemdReferenceScheme) || credentials == nil {
		return nil, errors.New("proxy bridge token must be a systemd credential reference")
	}
	if config.ProxyInstanceID == "" {
		return nil, errors.New("proxy instance ID is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	client := *httpClient
	client.Timeout = config.Timeout
	if client.Timeout <= 0 {
		client.Timeout = defaultBridgeTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base.Path = ""
	return &HTTPBridge{base: base, tokenRef: config.TokenRef, instanceID: config.ProxyInstanceID, credentials: credentials, client: &client, now: time.Now}, nil
}

type bridgeInfo struct {
	APIVersion      string   `json:"api_version"`
	ProxyPlatform   string   `json:"proxy_platform"`
	ProxyVersion    string   `json:"proxy_version"`
	ProxyInstanceID string   `json:"proxy_instance_id"`
	Features        []string `json:"features"`
}

// CheckInfo verifies that the bridge speaks API version 1 and is the
// configured proxy instance.
func (b *HTTPBridge) CheckInfo(ctx context.Context) error {
	var info bridgeInfo
	if _, err := b.do(ctx, http.MethodGet, "/v1/info", nil, &info); err != nil {
		return err
	}
	if info.APIVersion != bridgeAPIVersion {
		return fmt.Errorf("proxy bridge API version %q is not supported", info.APIVersion)
	}
	if info.ProxyInstanceID != b.instanceID {
		return errors.New("proxy bridge reports a different proxy instance")
	}
	return nil
}

type bridgeBackend struct {
	BackendID      string       `json:"backend_id"`
	Registration   string       `json:"registration"`
	InstanceRef    *ResourceRef `json:"instance_ref"`
	Address        *string      `json:"address"`
	Routable       bool         `json:"routable"`
	ActiveSessions *int64       `json:"active_sessions"`
	ObservedAt     time.Time    `json:"observed_at"`
}

func (b *HTTPBridge) backendPath(backendID string, suffix string) (string, error) {
	if !backendIDPattern.MatchString(backendID) {
		return "", errors.New("backend ID is not a valid proxy server name")
	}
	return "/v1/backends/" + backendID + suffix, nil
}

// InspectBackend implements Bridge.
func (b *HTTPBridge) InspectBackend(ctx context.Context, backendID string) (BackendObservation, error) {
	path, err := b.backendPath(backendID, "")
	if err != nil {
		return BackendObservation{}, err
	}
	var state bridgeBackend
	if _, err := b.do(ctx, http.MethodGet, path, nil, &state); err != nil {
		return BackendObservation{}, err
	}
	return b.observation(backendID, state)
}

func (b *HTTPBridge) observation(backendID string, state bridgeBackend) (BackendObservation, error) {
	if state.BackendID != backendID || state.ObservedAt.IsZero() {
		return BackendObservation{}, errBridgeResponse
	}
	observed := BackendObservation{InstanceRef: state.InstanceRef, Routable: state.Routable, ActiveSessions: state.ActiveSessions, ObservedAt: state.ObservedAt}
	if state.Address != nil {
		observed.Address = *state.Address
	}
	switch state.Registration {
	case "registered":
		observed.Registration = Registered
	case "draining":
		observed.Registration = Draining
		observed.Routable = false
	case "unregistered":
		observed.Registration = Unregistered
		observed.Routable = false
	default:
		return BackendObservation{}, errBridgeResponse
	}
	if observed.Registration != Unregistered && (observed.InstanceRef == nil || observed.Address == "") {
		return BackendObservation{}, errBridgeResponse
	}
	return observed, nil
}

// RegisterBackend implements Bridge.
func (b *HTTPBridge) RegisterBackend(ctx context.Context, target BackendTarget, address string) error {
	path, err := b.backendPath(target.BackendID, "")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotAttempted, err)
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return fmt.Errorf("%w: backend address is invalid", ErrNotAttempted)
	}
	body := map[string]any{"instance_ref": target.InstanceRef, "address": address}
	return b.mutate(ctx, http.MethodPut, path, body, target.BackendID)
}

// DrainBackend implements Bridge.
func (b *HTTPBridge) DrainBackend(ctx context.Context, target BackendTarget) error {
	path, err := b.backendPath(target.BackendID, "/drain")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotAttempted, err)
	}
	return b.mutate(ctx, http.MethodPost, path, nil, target.BackendID)
}

// UnregisterBackend implements Bridge.
func (b *HTTPBridge) UnregisterBackend(ctx context.Context, target BackendTarget) error {
	path, err := b.backendPath(target.BackendID, "")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotAttempted, err)
	}
	return b.mutate(ctx, http.MethodDelete, path, nil, target.BackendID)
}

func (b *HTTPBridge) mutate(ctx context.Context, method, path string, body any, backendID string) error {
	var state bridgeBackend
	if _, err := b.do(ctx, method, path, body, &state); err != nil {
		return err
	}
	// A 2xx reply that does not describe the requested backend leaves the
	// outcome unknown; the adapter confirms by inspecting.
	if _, err := b.observation(backendID, state); err != nil {
		return err
	}
	return nil
}

type bridgeSession struct {
	SessionID       string    `json:"session_id"`
	State           string    `json:"state"`
	BackendID       string    `json:"backend_id"`
	WaitingCohortID string    `json:"waiting_cohort_id"`
	ObservedAt      time.Time `json:"observed_at"`
}

func sessionPath(sessionID, suffix string) (string, error) {
	if sessionID == "" || len(sessionID) > 200 || strings.ContainsAny(sessionID, "/?#%") {
		return "", errors.New("session ID is invalid")
	}
	return "/v1/sessions/" + url.PathEscape(sessionID) + suffix, nil
}

// InspectSession implements Bridge. An unknown or expired session is
// reported as disconnected.
func (b *HTTPBridge) InspectSession(ctx context.Context, sessionID string) (SessionObservation, error) {
	path, err := sessionPath(sessionID, "")
	if err != nil {
		return SessionObservation{}, err
	}
	var session bridgeSession
	status, err := b.do(ctx, http.MethodGet, path, nil, &session)
	if status == http.StatusNotFound {
		return SessionObservation{State: Disconnected, ObservedAt: b.now().UTC()}, nil
	}
	if err != nil {
		return SessionObservation{}, err
	}
	if session.SessionID != sessionID || session.ObservedAt.IsZero() {
		return SessionObservation{}, errBridgeResponse
	}
	observed := SessionObservation{BackendID: session.BackendID, WaitingCohortID: session.WaitingCohortID, ObservedAt: session.ObservedAt}
	switch session.State {
	case "connected":
		if session.BackendID == "" {
			return SessionObservation{}, errBridgeResponse
		}
		observed.State = Connected
	case "disconnected":
		observed.State = Disconnected
		observed.BackendID = ""
	default:
		return SessionObservation{}, errBridgeResponse
	}
	return observed, nil
}

// TransferSession implements Bridge.
func (b *HTTPBridge) TransferSession(ctx context.Context, sessionID, destinationBackendID string) error {
	path, err := sessionPath(sessionID, "/transfer")
	if err != nil || !backendIDPattern.MatchString(destinationBackendID) {
		return fmt.Errorf("%w: invalid transfer request", ErrNotAttempted)
	}
	var reply struct {
		Outcome string `json:"outcome"`
	}
	if _, err := b.do(ctx, http.MethodPost, path, map[string]string{"destination_backend_id": destinationBackendID}, &reply); err != nil {
		return err
	}
	switch reply.Outcome {
	case "transferred":
		return nil
	case "not_attempted", "failed":
		// The player stayed where they were.
		return fmt.Errorf("%w: proxy reported %s", ErrNotAttempted, reply.Outcome)
	default:
		return errBridgeResponse
	}
}

// WaitingCohort lists the sessions waiting in one cohort.
func (b *HTTPBridge) WaitingCohort(ctx context.Context, cohortID string) ([]string, time.Time, error) {
	if !cohortIDPattern.MatchString(cohortID) {
		return nil, time.Time{}, errors.New("cohort ID is invalid")
	}
	var reply struct {
		CohortID   string    `json:"cohort_id"`
		SessionIDs []string  `json:"session_ids"`
		ObservedAt time.Time `json:"observed_at"`
	}
	if _, err := b.do(ctx, http.MethodGet, "/v1/cohorts/"+cohortID, nil, &reply); err != nil {
		return nil, time.Time{}, err
	}
	if reply.CohortID != cohortID || reply.ObservedAt.IsZero() || len(reply.SessionIDs) > 10000 {
		return nil, time.Time{}, errBridgeResponse
	}
	return reply.SessionIDs, reply.ObservedAt, nil
}

// do sends one request. It returns the HTTP status when a response arrived.
// A 4xx reply to any request is wrapped in ErrNotAttempted, because the
// bridge refuses requests before changing anything.
func (b *HTTPBridge) do(ctx context.Context, method, path string, body any, result any) (int, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("%w: could not encode request", ErrNotAttempted)
		}
		payload = bytes.NewReader(encoded)
	}
	endpoint := *b.base
	endpoint.Path = path
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), payload)
	if err != nil {
		return 0, fmt.Errorf("%w: could not build request", ErrNotAttempted)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if err := b.credentials.WithCredential(b.tokenRef, func(token []byte) error {
		value := bytes.TrimSpace(token)
		if len(value) == 0 || bytes.ContainsAny(value, "\r\n\t ") {
			return errors.New("invalid token")
		}
		request.Header.Set("Authorization", "Bearer "+string(value))
		return nil
	}); err != nil {
		return 0, fmt.Errorf("%w: proxy bridge token is unavailable", ErrNotAttempted)
	}
	response, err := b.client.Do(request)
	if err != nil {
		return 0, errBridgeTransport
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBridgeResponse+1))
	if err != nil || len(data) > maxBridgeResponse {
		return response.StatusCode, errBridgeResponse
	}
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
	case response.StatusCode >= 400 && response.StatusCode < 500:
		return response.StatusCode, fmt.Errorf("%w: %w (status %d)", ErrNotAttempted, errBridgeRefused, response.StatusCode)
	default:
		return response.StatusCode, errBridgeTransport
	}
	if err := json.Unmarshal(data, result); err != nil {
		return response.StatusCode, errBridgeResponse
	}
	return response.StatusCode, nil
}
