package cloudflaredns

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

type recordTarget struct {
	ZoneRef    ResourceRef `json:"zone_ref"`
	Name       string      `json:"name"`
	RecordType string      `json:"record_type"`
}

type readinessAssertion struct {
	AssertionID         string      `json:"assertion_id"`
	ServerID            string      `json:"server_id,omitempty"`
	EnvironmentID       string      `json:"environment_id"`
	InstanceRef         ResourceRef `json:"instance_ref,omitempty"`
	ReadinessCheckSetID string      `json:"readiness_check_set_id"`
	ObservedAt          time.Time   `json:"observed_at"`
	ExpiresAt           time.Time   `json:"expires_at"`
}

type inspectInput struct {
	Record        recordTarget `json:"record"`
	EnvironmentID string       `json:"environment_id"`
}

type upsertInput struct {
	Record             recordTarget        `json:"record"`
	EnvironmentID      string              `json:"environment_id"`
	Purpose            Purpose             `json:"purpose"`
	ServerID           string              `json:"server_id,omitempty"`
	InstanceRef        *ResourceRef        `json:"instance_ref,omitempty"`
	ReadinessAssertion *readinessAssertion `json:"readiness_assertion,omitempty"`
	Values             []string            `json:"values"`
	TTLSeconds         int                 `json:"ttl_seconds"`
	ExpectedValues     *[]string           `json:"expected_values,omitempty"`
}

type deleteInput struct {
	Record         recordTarget `json:"record"`
	EnvironmentID  string       `json:"environment_id"`
	ExpectedValues *[]string    `json:"expected_values,omitempty"`
}

// recordState is the provider's authoritative state for one name and type.
type recordState struct {
	ObservationState string    `json:"observation_state"`
	Values           []string  `json:"values,omitempty"`
	TTLSeconds       int       `json:"ttl_seconds,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
}

type inspectOutput struct {
	Record recordTarget `json:"record"`
	recordState
}

type actionOutput struct {
	Record         recordTarget `json:"record"`
	RequestOutcome string       `json:"request_outcome"`
	recordState
	ReasonCode string `json:"reason_code,omitempty"`
}

// observed is what one read of Cloudflare returned.
type observed struct {
	records []dnsRecord
	state   recordState
	proxied bool
}

func decode(input json.RawMessage, target any) error {
	if err := json.Unmarshal(input, target); err != nil {
		return errors.New("invalid Cloudflare DNS capability input")
	}
	return nil
}

// checkTarget enforces environment, zone, and managed-name binding. These are
// request errors, so nothing is sent to Cloudflare.
func (a *Adapter) checkTarget(record recordTarget, environmentID string) (ManagedRecord, error) {
	if environmentID != a.config.EnvironmentID || record.ZoneRef != a.zoneRef {
		return ManagedRecord{}, errors.New("DNS record target is outside this adapter's zone or environment")
	}
	managed, ok := a.records[record.Name]
	if !ok {
		return ManagedRecord{}, errors.New("DNS record name is not managed by this adapter")
	}
	return managed, nil
}

func (a *Adapter) withToken(use func([]byte) error) error {
	err := a.credentials.WithCredential(a.config.APITokenRef, use)
	if err != nil && !errors.Is(err, errRefused) && !errors.Is(err, errUncertain) {
		return errRefused
	}
	return err
}

// read returns the current records for the target, or an unknown state.
func (a *Adapter) read(ctx context.Context, record recordTarget) (observed, error) {
	var records []dnsRecord
	err := a.withToken(func(token []byte) error {
		var err error
		records, err = a.client.records(ctx, token, record.Name, record.RecordType)
		return err
	})
	if err != nil {
		return observed{state: a.unknownState()}, err
	}
	result := observed{records: records, state: recordState{ObservationState: "absent", ObservedAt: a.now().UTC()}}
	if len(records) == 0 {
		return result, nil
	}
	values := make([]string, 0, len(records))
	ttl := 0
	for _, entry := range records {
		ip, err := netip.ParseAddr(entry.Content)
		if err != nil {
			return observed{state: a.unknownState()}, errUncertain
		}
		values = append(values, ip.String())
		recordTTL := entry.TTL
		if recordTTL == 1 {
			recordTTL = 300 // Cloudflare's "automatic" TTL
		}
		if ttl == 0 || recordTTL < ttl {
			ttl = recordTTL
		}
		result.proxied = result.proxied || entry.Proxied
	}
	sort.Strings(values)
	if len(values) > 8 || ttl < 30 || ttl > 86400 {
		return observed{state: a.unknownState()}, errUncertain
	}
	result.state = recordState{ObservationState: "present", Values: values, TTLSeconds: ttl, ObservedAt: a.now().UTC()}
	return result, nil
}

func (a *Adapter) unknownState() recordState {
	return recordState{ObservationState: "unknown", ObservedAt: a.now().UTC()}
}

func (a *Adapter) inspect(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var input inspectInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	if _, err := a.checkTarget(input.Record, input.EnvironmentID); err != nil {
		return nil, "", err
	}
	current, _ := a.read(ctx, input.Record)
	return inspectOutput{Record: input.Record, recordState: current.state}, adapter.OutcomeCompleted, nil
}

func (a *Adapter) rejected(record recordTarget, state recordState, reason string) actionOutput {
	return actionOutput{Record: record, RequestOutcome: "rejected", recordState: state, ReasonCode: reason}
}

func (a *Adapter) unknown(record recordTarget, reason string) (any, adapter.Outcome, error) {
	return actionOutput{Record: record, RequestOutcome: "outcome_unknown", recordState: a.unknownState(), ReasonCode: reason}, adapter.OutcomeUnknown, nil
}

// readinessProblem returns a reason code unless the assertion is current and
// bound to the request's server, environment, and instance.
func (a *Adapter) readinessProblem(input upsertInput) string {
	assertion := input.ReadinessAssertion
	if assertion == nil || input.InstanceRef == nil {
		return "readiness_assertion_required"
	}
	if assertion.ServerID != input.ServerID || assertion.EnvironmentID != input.EnvironmentID || assertion.InstanceRef != *input.InstanceRef {
		return "readiness_assertion_mismatch"
	}
	now := a.now()
	switch {
	case assertion.ObservedAt.IsZero() || !assertion.ExpiresAt.After(assertion.ObservedAt):
		return "readiness_assertion_invalid"
	case assertion.ObservedAt.After(now):
		return "readiness_assertion_not_yet_valid"
	case !assertion.ExpiresAt.After(now):
		return "readiness_assertion_expired"
	}
	return ""
}

// policyProblem applies the protocol purpose rules and this adapter's
// configuration. It runs before any Cloudflare call.
func (a *Adapter) policyProblem(input upsertInput, managed ManagedRecord, values []string) string {
	allowed := false
	for _, purpose := range managed.Purposes {
		allowed = allowed || purpose == input.Purpose
	}
	if !allowed {
		return "purpose_not_allowed"
	}
	if input.TTLSeconds < minimumTTLSeconds {
		return "ttl_below_provider_minimum"
	}
	switch input.Purpose {
	case PurposeBackendAddress, PurposeDirectJoin:
		if input.InstanceRef == nil || input.InstanceRef.EnvironmentID != input.EnvironmentID || input.ServerID == "" {
			return "instance_outside_environment"
		}
		if input.Purpose == PurposeDirectJoin {
			return a.readinessProblem(input)
		}
	case PurposeFallback:
		if !sameValues(values, managed.Fallback[input.Record.RecordType]) {
			return "fallback_values_mismatch"
		}
	default:
		return "purpose_not_allowed"
	}
	return ""
}

func (a *Adapter) upsert(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var input upsertInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	managed, err := a.checkTarget(input.Record, input.EnvironmentID)
	if err != nil {
		return nil, "", err
	}
	values, ok := canonicalValues(input.Record.RecordType, input.Values)
	if !ok || len(values) == 0 {
		return nil, "", errors.New("DNS record values must be unique IP addresses matching the record type")
	}
	if reason := a.policyProblem(input, managed, values); reason != "" {
		// Report the current state if it can be read; nothing is changed.
		current, _ := a.read(ctx, input.Record)
		return a.rejected(input.Record, current.state, reason), adapter.OutcomeCompleted, nil
	}
	current, err := a.read(ctx, input.Record)
	if err != nil {
		return a.rejected(input.Record, current.state, "provider_unavailable"), adapter.OutcomeCompleted, nil
	}
	if reason := expectationProblem(current, input.Record.RecordType, input.ExpectedValues); reason != "" {
		return a.rejected(input.Record, current.state, reason), adapter.OutcomeCompleted, nil
	}
	if current.state.ObservationState == "present" && sameValues(current.state.Values, values) && current.state.TTLSeconds == input.TTLSeconds && allTTL(current.records, input.TTLSeconds) {
		return actionOutput{Record: input.Record, RequestOutcome: "confirmed", recordState: current.state}, adapter.OutcomeCompleted, nil
	}

	changed := false
	apply := func(step func([]byte) error) error {
		err := a.withToken(step)
		if err == nil {
			changed = true
		}
		return err
	}
	keep := map[string]bool{}
	for _, value := range values {
		keep[value] = true
	}
	existing := map[string]bool{}
	var stepErr error
	// Create missing values first so the name never resolves to nothing.
	for _, entry := range current.records {
		ip, _ := netip.ParseAddr(entry.Content)
		existing[ip.String()] = true
	}
	for _, value := range values {
		if stepErr == nil && !existing[value] {
			record := dnsRecord{Type: input.Record.RecordType, Name: input.Record.Name, Content: value, TTL: input.TTLSeconds}
			stepErr = apply(func(token []byte) error { return a.client.create(ctx, token, record) })
		}
	}
	for _, entry := range current.records {
		ip, _ := netip.ParseAddr(entry.Content)
		switch {
		case stepErr != nil:
		case !keep[ip.String()]:
			id := entry.ID
			stepErr = apply(func(token []byte) error { return a.client.delete(ctx, token, id) })
		case entry.TTL != input.TTLSeconds:
			id := entry.ID
			stepErr = apply(func(token []byte) error { return a.client.patch(ctx, token, id, input.TTLSeconds) })
		}
	}
	if stepErr != nil {
		if errors.Is(stepErr, errRefused) && !changed {
			return a.rejected(input.Record, current.state, "provider_rejected"), adapter.OutcomeCompleted, nil
		}
		return a.unknown(input.Record, "provider_outcome_unknown")
	}
	return a.confirm(ctx, input.Record, func(state recordState, records []dnsRecord) bool {
		return state.ObservationState == "present" && sameValues(state.Values, values) && allTTL(records, input.TTLSeconds)
	})
}

func (a *Adapter) remove(ctx context.Context, raw json.RawMessage) (any, adapter.Outcome, error) {
	var input deleteInput
	if err := decode(raw, &input); err != nil {
		return nil, "", err
	}
	if _, err := a.checkTarget(input.Record, input.EnvironmentID); err != nil {
		return nil, "", err
	}
	current, err := a.read(ctx, input.Record)
	if err != nil {
		return a.rejected(input.Record, current.state, "provider_unavailable"), adapter.OutcomeCompleted, nil
	}
	if reason := expectationProblem(current, input.Record.RecordType, input.ExpectedValues); reason != "" {
		return a.rejected(input.Record, current.state, reason), adapter.OutcomeCompleted, nil
	}
	if current.state.ObservationState == "absent" {
		return actionOutput{Record: input.Record, RequestOutcome: "confirmed", recordState: current.state}, adapter.OutcomeCompleted, nil
	}
	for i, entry := range current.records {
		id := entry.ID
		if err := a.withToken(func(token []byte) error { return a.client.delete(ctx, token, id) }); err != nil {
			if errors.Is(err, errRefused) && i == 0 {
				return a.rejected(input.Record, current.state, "provider_rejected"), adapter.OutcomeCompleted, nil
			}
			return a.unknown(input.Record, "provider_outcome_unknown")
		}
	}
	return a.confirm(ctx, input.Record, func(state recordState, _ []dnsRecord) bool {
		return state.ObservationState == "absent"
	})
}

// confirm re-reads the record after a change. Only an observation matching
// the requested postcondition is confirmation.
func (a *Adapter) confirm(ctx context.Context, record recordTarget, done func(recordState, []dnsRecord) bool) (any, adapter.Outcome, error) {
	after, err := a.read(ctx, record)
	if err != nil {
		return actionOutput{Record: record, RequestOutcome: "accepted", recordState: after.state, ReasonCode: "confirmation_unavailable"}, adapter.OutcomeAccepted, nil
	}
	if done(after.state, after.records) && !after.proxied {
		return actionOutput{Record: record, RequestOutcome: "confirmed", recordState: after.state}, adapter.OutcomeCompleted, nil
	}
	return actionOutput{Record: record, RequestOutcome: "accepted", recordState: after.state, ReasonCode: "postcondition_not_observed"}, adapter.OutcomeAccepted, nil
}

// expectationProblem refuses changes to proxied records and enforces the
// optional expected_values check.
func expectationProblem(current observed, recordType string, expected *[]string) string {
	if current.proxied {
		return "proxied_record_present"
	}
	if expected == nil {
		return ""
	}
	want, ok := canonicalValues(recordType, *expected)
	if !ok {
		return "current_values_mismatch"
	}
	have := current.state.Values
	if current.state.ObservationState == "absent" {
		have = []string{}
	}
	if !sameValues(have, want) {
		return "current_values_mismatch"
	}
	return ""
}

func allTTL(records []dnsRecord, ttl int) bool {
	for _, entry := range records {
		if entry.TTL != ttl || entry.Proxied {
			return false
		}
	}
	return true
}
