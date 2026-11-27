package velocityproxy

import (
	"context"
	"encoding/json"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

type playersInput struct {
	Target            BackendTarget `json:"target"`
	MaximumAgeSeconds int           `json:"maximum_age_seconds"`
}

type playersOutput struct {
	Target             BackendTarget `json:"target"`
	ObservationState   string        `json:"observation_state"`
	ActiveSessionCount *int64        `json:"active_session_count,omitempty"`
	ObservedAt         *time.Time    `json:"observed_at,omitempty"`
}

func (input playersInput) backendTarget() BackendTarget { return input.Target }

func (a *Adapter) inspectPlayers(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	input, err := decodeTargeted[playersInput](a, raw)
	if err != nil {
		return nil, "", err
	}
	unknown := playersOutput{Target: input.Target, ObservationState: "unknown"}
	observed, err := a.bridge.InspectBackend(ctx, input.Target.BackendID)
	count := knownSessionCount(observed)
	if err != nil || count == nil || observed.ObservedAt.IsZero() {
		return unknown, adapter.OutcomeCompleted, nil
	}
	if !a.viewBackend(ctx, input.Target, observed).boundToInstance() {
		return unknown, adapter.OutcomeCompleted, nil
	}
	if !a.fresh(observed.ObservedAt, input.MaximumAgeSeconds) {
		return unknown, adapter.OutcomeCompleted, nil
	}
	at := observed.ObservedAt.UTC()
	return playersOutput{Target: input.Target, ObservationState: "observed", ActiveSessionCount: count, ObservedAt: &at}, adapter.OutcomeCompleted, nil
}

type sessionInput struct {
	ProxyInstanceRef             ResourceRef `json:"proxy_instance_ref"`
	ProxySessionID               string      `json:"proxy_session_id"`
	MaximumObservationAgeSeconds int         `json:"maximum_observation_age_seconds"`
}

func (input sessionInput) proxyRef() ResourceRef { return input.ProxyInstanceRef }

type sessionOutput struct {
	ProxyInstanceRef ResourceRef `json:"proxy_instance_ref"`
	ProxySessionID   string      `json:"proxy_session_id"`
	ConnectionState  string      `json:"connection_state"`
	CurrentBackendID string      `json:"current_backend_id,omitempty"`
	ObservedAt       time.Time   `json:"observed_at"`
}

func (a *Adapter) inspectSession(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	input, err := decodeForProxy[sessionInput](a, raw, "session")
	if err != nil {
		return nil, "", err
	}
	output := sessionOutput{ProxyInstanceRef: input.ProxyInstanceRef, ProxySessionID: input.ProxySessionID, ConnectionState: "unknown", ObservedAt: a.now().UTC()}
	observed, err := a.bridge.InspectSession(ctx, input.ProxySessionID)
	if err != nil || !a.fresh(observed.ObservedAt, input.MaximumObservationAgeSeconds) {
		return output, adapter.OutcomeCompleted, nil
	}
	output.ObservedAt = observed.ObservedAt.UTC()
	switch {
	case observed.State == Connected && observed.BackendID != "" && len(observed.BackendID) <= 200:
		output.ConnectionState, output.CurrentBackendID = "connected", observed.BackendID
	case observed.State == Disconnected:
		output.ConnectionState = "disconnected"
	}
	return output, adapter.OutcomeCompleted, nil
}

type cohortInput struct {
	ProxyInstanceRef             ResourceRef `json:"proxy_instance_ref"`
	WaitingCohortID              string      `json:"waiting_cohort_id"`
	MaximumSessions              int         `json:"maximum_sessions"`
	MaximumObservationAgeSeconds int         `json:"maximum_observation_age_seconds"`
}

func (input cohortInput) proxyRef() ResourceRef { return input.ProxyInstanceRef }

type cohortOutput struct {
	ProxyInstanceRef ResourceRef `json:"proxy_instance_ref"`
	WaitingCohortID  string      `json:"waiting_cohort_id"`
	ObservationState string      `json:"observation_state"`
	SessionCount     *int        `json:"session_count,omitempty"`
	PlayerSessionIDs *[]string   `json:"player_session_ids,omitempty"`
	Truncated        *bool       `json:"truncated,omitempty"`
	ObservedAt       *time.Time  `json:"observed_at,omitempty"`
}

func (a *Adapter) inspectCohort(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	input, err := decodeForProxy[cohortInput](a, raw, "cohort")
	if err != nil {
		return nil, "", err
	}
	unknown := cohortOutput{ProxyInstanceRef: input.ProxyInstanceRef, WaitingCohortID: input.WaitingCohortID, ObservationState: "unknown"}
	ids, observedAt, err := a.bridge.WaitingCohort(ctx, input.WaitingCohortID)
	if err != nil || !a.fresh(observedAt, input.MaximumObservationAgeSeconds) || input.MaximumSessions < 1 {
		return unknown, adapter.OutcomeCompleted, nil
	}
	if !uniqueSessionIDs(ids) {
		return unknown, adapter.OutcomeCompleted, nil
	}
	count := len(ids)
	listed := ids
	if len(listed) > input.MaximumSessions {
		listed = listed[:input.MaximumSessions]
	}
	truncated := len(listed) < count
	at := observedAt.UTC()
	listedCopy := append([]string{}, listed...)
	output := cohortOutput{ProxyInstanceRef: input.ProxyInstanceRef, WaitingCohortID: input.WaitingCohortID, ObservationState: "observed",
		SessionCount: &count, PlayerSessionIDs: &listedCopy, Truncated: &truncated, ObservedAt: &at}
	return output, adapter.OutcomeCompleted, nil
}
