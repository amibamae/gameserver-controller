package velocityproxy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

// Transfer outcomes defined by the velocity-player-transfer-result schema.
const (
	outcomeTransferred          = "transferred"
	outcomeAlreadyAtDestination = "already_at_destination"
	outcomeDisconnected         = "disconnected"
	outcomeRejected             = "rejected"
	outcomeFailed               = "failed"
	outcomeUnknown              = "outcome_unknown"
)

// sessionObservationMaxAgeSeconds bounds how old a session observation may be
// before the adapter refuses to act on it.
const sessionObservationMaxAgeSeconds = 10

type transferRequest struct {
	ServerID             string             `json:"server_id"`
	EnvironmentID        string             `json:"environment_id"`
	SourceBackendID      string             `json:"source_backend_id"`
	DestinationBackendID string             `json:"destination_backend_id"`
	WaitingCohortID      string             `json:"waiting_cohort_id"`
	ExpectedInstanceRef  ResourceRef        `json:"expected_instance_ref"`
	ReadinessAssertion   ReadinessAssertion `json:"readiness_assertion"`
	PlayerSessionIDs     []string           `json:"player_session_ids"`
}

type protocolError struct {
	Code      string `json:"code"`
	Category  string `json:"category"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type sessionResult struct {
	ProxySessionID string         `json:"proxy_session_id"`
	Outcome        string         `json:"outcome"`
	Error          *protocolError `json:"error,omitempty"`
}

type outcomeCounts struct {
	Transferred          int `json:"transferred"`
	AlreadyAtDestination int `json:"already_at_destination"`
	Disconnected         int `json:"disconnected"`
	Rejected             int `json:"rejected"`
	Failed               int `json:"failed"`
	OutcomeUnknown       int `json:"outcome_unknown"`
}

type transferResult struct {
	WaitingCohortID      string          `json:"waiting_cohort_id"`
	SourceBackendID      string          `json:"source_backend_id"`
	DestinationBackendID string          `json:"destination_backend_id"`
	TargetInstanceRef    ResourceRef     `json:"target_instance_ref"`
	RequestedCount       int             `json:"requested_count"`
	OutcomeCounts        outcomeCounts   `json:"outcome_counts"`
	Results              []sessionResult `json:"results"`
}

// Per-session errors use fixed messages so no session ID, player identity,
// or bridge diagnostic reaches the result.
var (
	errReadinessMismatch  = protocolError{Code: "readiness_assertion_mismatch", Category: "safety_blocked", Message: "Readiness assertion is not bound to the requested server, environment, and instance."}
	errReadinessExpired   = protocolError{Code: "readiness_assertion_expired", Category: "safety_blocked", Message: "Readiness assertion has expired."}
	errReadinessNotValid  = protocolError{Code: "readiness_assertion_not_yet_valid", Category: "safety_blocked", Message: "Readiness assertion is not yet valid."}
	errReadinessInvalid   = protocolError{Code: "readiness_assertion_invalid", Category: "validation", Message: "Readiness assertion has an invalid validity window."}
	errDestinationRouting = protocolError{Code: "destination_not_routable", Category: "safety_blocked", Message: "Destination backend is not registered and routable for the expected instance."}
	errDestinationUnknown = protocolError{Code: "destination_state_unavailable", Category: "transient", Message: "Destination backend state could not be observed.", Retryable: true}
	errSameBackend        = protocolError{Code: "source_is_destination", Category: "validation", Message: "Source and destination backends must differ."}
	errNotInCohort        = protocolError{Code: "session_not_in_cohort", Category: "safety_blocked", Message: "Session is not a member of the requested waiting cohort."}
	errNotOnSource        = protocolError{Code: "session_not_on_source_backend", Category: "safety_blocked", Message: "Session is not connected to the declared source backend."}
	errSessionUnavailable = protocolError{Code: "session_state_unavailable", Category: "transient", Message: "Session state could not be freshly observed.", Retryable: true}
	errNotAttempted       = protocolError{Code: "transfer_not_attempted", Category: "provider_failure", Message: "Proxy did not attempt the transfer.", Retryable: true}
	errDeadline           = protocolError{Code: "deadline_exceeded", Category: "timeout", Message: "Invocation deadline reached before the transfer was attempted.", Retryable: true}
	errTransferUnknown    = protocolError{Code: "transfer_outcome_unknown", Category: "provider_failure", Message: "Transfer may have occurred; inspect the session before retrying."}
)

var readinessErrors = map[string]protocolError{
	"readiness_assertion_mismatch":      errReadinessMismatch,
	"readiness_assertion_expired":       errReadinessExpired,
	"readiness_assertion_not_yet_valid": errReadinessNotValid,
	"readiness_assertion_invalid":       errReadinessInvalid,
}

func (a *Adapter) transferPlayers(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var request transferRequest
	if err := decode(raw, &request); err != nil {
		return nil, "", err
	}
	if request.EnvironmentID != a.config.EnvironmentID || request.ExpectedInstanceRef.EnvironmentID != request.EnvironmentID {
		return nil, "", errors.New("transfer request is outside this Velocity adapter's environment")
	}
	ids := request.PlayerSessionIDs
	if len(ids) == 0 || len(ids) > maxSessionsPerTransfer {
		return nil, "", errors.New("transfer request must name between 1 and 100 sessions")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, "", errors.New("transfer session IDs must be unique and non-empty")
		}
		seen[id] = true
	}

	results := make([]sessionResult, len(ids))
	rejectAll := func(problem protocolError, outcome string) {
		for i, id := range ids {
			results[i] = sessionResult{ProxySessionID: id, Outcome: outcome, Error: errorRef(problem)}
		}
	}
	readiness := a.readinessProblem(request.ReadinessAssertion, request.ServerID, request.EnvironmentID, request.ExpectedInstanceRef)
	switch {
	case request.SourceBackendID == request.DestinationBackendID:
		rejectAll(errSameBackend, outcomeRejected)
	case readiness != "":
		rejectAll(readinessErrors[readiness], outcomeRejected)
	default:
		destination := BackendTarget{ServerID: request.ServerID, EnvironmentID: request.EnvironmentID, BackendID: request.DestinationBackendID, InstanceRef: request.ExpectedInstanceRef}
		observed, err := a.bridge.InspectBackend(ctx, destination.BackendID)
		if err != nil {
			rejectAll(errDestinationUnknown, outcomeFailed)
			break
		}
		if state, routability := a.boundState(ctx, destination, observed); state != Registered || routability != "routable" {
			rejectAll(errDestinationRouting, outcomeRejected)
			break
		}
		for i, id := range ids {
			results[i] = a.transferOne(ctx, request, id)
		}
	}

	output := transferResult{
		WaitingCohortID: request.WaitingCohortID, SourceBackendID: request.SourceBackendID,
		DestinationBackendID: request.DestinationBackendID, TargetInstanceRef: request.ExpectedInstanceRef,
		RequestedCount: len(ids), Results: results,
	}
	for _, result := range results {
		output.OutcomeCounts.add(result.Outcome)
	}
	if err := validateTransferResult(request, output); err != nil {
		return nil, "", err
	}
	if output.OutcomeCounts.OutcomeUnknown > 0 {
		return output, adapter.OutcomeUnknown, nil
	}
	return output, adapter.OutcomeCompleted, nil
}

// transferOne verifies one session against the request and, only if it is
// still connected to the source backend in the requested cohort, moves it.
func (a *Adapter) transferOne(ctx context.Context, request transferRequest, id string) sessionResult {
	result := func(outcome string, problem *protocolError) sessionResult {
		return sessionResult{ProxySessionID: id, Outcome: outcome, Error: problem}
	}
	if ctx.Err() != nil {
		return result(outcomeFailed, errorRef(errDeadline))
	}
	observed, err := a.bridge.InspectSession(ctx, id)
	if err != nil || !a.fresh(observed.ObservedAt, sessionObservationMaxAgeSeconds) {
		return result(outcomeFailed, errorRef(errSessionUnavailable))
	}
	switch observed.State {
	case Disconnected:
		return result(outcomeDisconnected, nil)
	case Connected:
	default:
		return result(outcomeFailed, errorRef(errSessionUnavailable))
	}
	if observed.WaitingCohortID != request.WaitingCohortID {
		return result(outcomeRejected, errorRef(errNotInCohort))
	}
	if observed.BackendID == request.DestinationBackendID {
		return result(outcomeAlreadyAtDestination, nil)
	}
	if observed.BackendID != request.SourceBackendID {
		return result(outcomeRejected, errorRef(errNotOnSource))
	}
	// The assertion can expire during a long batch; recheck before each move.
	if reason := a.readinessProblem(request.ReadinessAssertion, request.ServerID, request.EnvironmentID, request.ExpectedInstanceRef); reason != "" {
		return result(outcomeRejected, errorRef(readinessErrors[reason]))
	}
	if ctx.Err() != nil {
		return result(outcomeFailed, errorRef(errDeadline))
	}
	if err := a.bridge.TransferSession(ctx, id, request.DestinationBackendID); err != nil {
		if errors.Is(err, ErrNotAttempted) {
			return result(outcomeFailed, errorRef(errNotAttempted))
		}
		return result(outcomeUnknown, errorRef(errTransferUnknown))
	}
	return result(outcomeTransferred, nil)
}

func errorRef(problem protocolError) *protocolError {
	return &problem
}

func (c *outcomeCounts) add(outcome string) {
	switch outcome {
	case outcomeTransferred:
		c.Transferred++
	case outcomeAlreadyAtDestination:
		c.AlreadyAtDestination++
	case outcomeDisconnected:
		c.Disconnected++
	case outcomeRejected:
		c.Rejected++
	case outcomeFailed:
		c.Failed++
	case outcomeUnknown:
		c.OutcomeUnknown++
	}
}

func (c outcomeCounts) total() int {
	return c.Transferred + c.AlreadyAtDestination + c.Disconnected + c.Rejected + c.Failed + c.OutcomeUnknown
}

// validateTransferResult checks the cross-field invariants JSON Schema cannot
// express: one result per requested session, matching counts, and a result
// bound to the requested cohort, backends, and instance.
func validateTransferResult(request transferRequest, result transferResult) error {
	if result.WaitingCohortID != request.WaitingCohortID || result.SourceBackendID != request.SourceBackendID ||
		result.DestinationBackendID != request.DestinationBackendID || result.TargetInstanceRef != request.ExpectedInstanceRef {
		return errors.New("transfer result is not bound to the request")
	}
	if result.RequestedCount != len(request.PlayerSessionIDs) || len(result.Results) != result.RequestedCount {
		return errors.New("transfer result does not report every requested session")
	}
	ids := make([]string, len(result.Results))
	var counts outcomeCounts
	for i, entry := range result.Results {
		ids[i] = entry.ProxySessionID
		counts.add(entry.Outcome)
	}
	if !sameStrings(ids, request.PlayerSessionIDs) {
		return errors.New("transfer result sessions do not match the request")
	}
	if counts != result.OutcomeCounts || counts.total() != result.RequestedCount {
		return errors.New("transfer result counts do not match per-session outcomes")
	}
	return nil
}
