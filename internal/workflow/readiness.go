package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const maxReadinessAssertionTTL = 15 * time.Minute

type readinessCheckInput struct {
	ServerID            string                 `json:"server_id"`
	EnvironmentID       string                 `json:"environment_id"`
	InstanceRef         json.RawMessage        `json:"instance_ref"`
	GameReleaseID       string                 `json:"game_release_id"`
	ReadinessCheckSetID string                 `json:"readiness_check_set_id"`
	RequiredChecks      []ReadinessRequirement `json:"required_checks"`
}

type ReadinessRequirement struct {
	CheckID      string `json:"check_id"`
	CheckVersion string `json:"check_version"`
}

type readinessCheckOutput struct {
	ServerID      string                 `json:"server_id"`
	EnvironmentID string                 `json:"environment_id"`
	InstanceRef   json.RawMessage        `json:"instance_ref"`
	GameReleaseID string                 `json:"game_release_id"`
	ObservedAt    string                 `json:"observed_at"`
	CheckResults  []readinessCheckResult `json:"check_results"`
}

type readinessCheckResult struct {
	CheckID      string `json:"check_id"`
	CheckVersion string `json:"check_version"`
	Result       string `json:"result"`
	ObservedAt   string `json:"observed_at"`
}

type readinessAssertion struct {
	AssertionID         string          `json:"assertion_id"`
	ServerID            string          `json:"server_id"`
	EnvironmentID       string          `json:"environment_id"`
	InstanceRef         json.RawMessage `json:"instance_ref"`
	ReadinessCheckSetID string          `json:"readiness_check_set_id"`
	ObservedAt          string          `json:"observed_at"`
	ExpiresAt           string          `json:"expires_at"`
}

// BuildReadinessAssertion turns a stored game readiness observation into a
// short-lived controller assertion. The step ID is resolved from the supplied
// operation snapshot; callers must load that record from the durable store.
// Every requested check must pass, and the assertion is bound to that exact
// server, environment, release, and managed instance.
func BuildReadinessAssertion(schemas schemaRuntime, operation store.OperationRecord, server store.LogicalServer, stepID string, ttl time.Duration) (json.RawMessage, error) {
	return buildReadinessAssertionAt(schemas, operation, server, stepID, ttl, time.Now().UTC())
}

func buildReadinessAssertionAt(schemas schemaRuntime, operation store.OperationRecord, server store.LogicalServer, stepID string, ttl time.Duration, now time.Time) (json.RawMessage, error) {
	step, err := verifiedReadinessStep(schemas, operation, server, stepID, ttl, now)
	if err != nil {
		return nil, err
	}
	input, output, assertionSchema, err := decodeReadinessEvidence(schemas, step)
	if err != nil {
		return nil, err
	}
	aggregateObservedAt, expiresAt, checkVersions, err := validateReadinessObservations(input, output, operation, server, ttl, now)
	if err != nil {
		return nil, err
	}
	checkDigest := sha256.Sum256([]byte(input.ReadinessCheckSetID + "\x00" + joinCheckVersions(checkVersions)))
	outputDigest := sha256.Sum256(step.Output)
	assertionDigest := sha256.Sum256([]byte(operation.OperationID + "\x00" + step.StepID + "\x00" + hex.EncodeToString(outputDigest[:]) + "\x00" + hex.EncodeToString(checkDigest[:])))
	assertion := readinessAssertion{
		AssertionID:         "readiness-" + hex.EncodeToString(assertionDigest[:]),
		ServerID:            operation.Target.ServerID,
		EnvironmentID:       operation.Target.EnvironmentID,
		InstanceRef:         append(json.RawMessage(nil), server.InstanceRef...),
		ReadinessCheckSetID: input.ReadinessCheckSetID,
		ObservedAt:          aggregateObservedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:           expiresAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(assertion)
	if err != nil {
		return nil, errors.New("encode readiness assertion")
	}
	if err := schemas.ValidateJSON(assertionSchema, encoded); err != nil {
		return nil, fmt.Errorf("readiness assertion does not match protocol schema: %w", err)
	}
	return encoded, nil
}

func verifiedReadinessStep(schemas schemaRuntime, operation store.OperationRecord, server store.LogicalServer, stepID string, ttl time.Duration, now time.Time) (store.OperationStep, error) {
	if schemas == nil || ttl <= 0 || ttl > maxReadinessAssertionTTL {
		return store.OperationStep{}, errors.New("schema registry and readiness assertion TTL (1ns..15m) are required")
	}
	if now.IsZero() {
		return store.OperationStep{}, errors.New("readiness assertion clock is required")
	}
	if operation.OperationType != "server.start" && operation.OperationType != "server.provision" && operation.OperationType != "server.restore" {
		return store.OperationStep{}, errors.New("readiness assertions are only valid for start, provision, and restore operations")
	}
	if operation.ProtocolVersion == "" || operation.ProtocolVersion != schemas.ProtocolVersion() {
		return store.OperationStep{}, errors.New("readiness operation protocol version is not loaded by the schema registry")
	}
	if operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" ||
		server.ServerID != operation.Target.ServerID || server.EnvironmentID != operation.Target.EnvironmentID ||
		server.LifecycleState == protocolstate.ServerDestroyed || len(server.InstanceRef) == 0 {
		return store.OperationStep{}, errors.New("readiness target does not match an existing managed instance")
	}
	if len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, server.InstanceRef) {
		return store.OperationStep{}, errors.New("readiness target instance does not match the managed instance")
	}
	var step store.OperationStep
	for _, persisted := range operation.Steps {
		if persisted.StepID == stepID && stepID != "" {
			step = persisted
			break
		}
	}
	if step.StepID == "" {
		return store.OperationStep{}, errors.New("readiness evidence step is not part of the persisted operation")
	}
	if step.Status != protocolstate.StepSucceeded || step.StepType != "game.readiness.check" || step.AdapterID == "" ||
		step.CapabilityName != "game.readiness.check" || step.CapabilityVersion == "" ||
		len(step.Input) == 0 || len(step.Output) == 0 {
		return store.OperationStep{}, errors.New("readiness evidence must come from a completed game.readiness.check step")
	}
	return step, nil
}

func decodeReadinessEvidence(schemas schemaRuntime, step store.OperationStep) (readinessCheckInput, readinessCheckOutput, string, error) {
	version := schemas.ProtocolVersion()
	if version == "" {
		return readinessCheckInput{}, readinessCheckOutput{}, "", errors.New("schema registry has no protocol version")
	}
	inputSchema := "urn:gameserver-controller:" + version + ":game:readiness-check-input"
	outputSchema := "urn:gameserver-controller:" + version + ":game:readiness-check-output"
	assertionSchema := "urn:gameserver-controller:" + version + ":lifecycle:readiness-assertion"
	readinessInput := step.Input
	if len(step.ResolvedInput) > 0 {
		readinessInput = step.ResolvedInput
	}
	if err := schemas.ValidateJSON(inputSchema, readinessInput); err != nil {
		return readinessCheckInput{}, readinessCheckOutput{}, "", fmt.Errorf("readiness request does not match protocol schema: %w", err)
	}
	if err := schemas.ValidateJSON(outputSchema, step.Output); err != nil {
		return readinessCheckInput{}, readinessCheckOutput{}, "", fmt.Errorf("readiness observation does not match protocol schema: %w", err)
	}
	var input readinessCheckInput
	if err := json.Unmarshal(readinessInput, &input); err != nil {
		return readinessCheckInput{}, readinessCheckOutput{}, "", errors.New("decode readiness request")
	}
	var output readinessCheckOutput
	if err := json.Unmarshal(step.Output, &output); err != nil {
		return readinessCheckInput{}, readinessCheckOutput{}, "", errors.New("decode readiness observation")
	}
	return input, output, assertionSchema, nil
}

func validateReadinessObservations(input readinessCheckInput, output readinessCheckOutput, operation store.OperationRecord, server store.LogicalServer, ttl time.Duration, now time.Time) (time.Time, time.Time, []string, error) {
	if input.ServerID != operation.Target.ServerID || input.EnvironmentID != operation.Target.EnvironmentID ||
		output.ServerID != operation.Target.ServerID || output.EnvironmentID != operation.Target.EnvironmentID ||
		input.GameReleaseID == "" || output.GameReleaseID != input.GameReleaseID ||
		!equalJSON(input.InstanceRef, server.InstanceRef) || !equalJSON(output.InstanceRef, server.InstanceRef) {
		return time.Time{}, time.Time{}, nil, errors.New("readiness evidence is not bound to the requested server, release, and managed instance")
	}
	if input.ReadinessCheckSetID == "" || len(input.RequiredChecks) == 0 || len(output.CheckResults) != len(input.RequiredChecks) {
		return time.Time{}, time.Time{}, nil, errors.New("readiness evidence must contain exactly one result for every required check")
	}
	required := make(map[string]string, len(input.RequiredChecks))
	for _, requirement := range input.RequiredChecks {
		if requirement.CheckID == "" || requirement.CheckVersion == "" {
			return time.Time{}, time.Time{}, nil, errors.New("readiness request contains an empty required check ID or version")
		}
		if _, duplicate := required[requirement.CheckID]; duplicate {
			return time.Time{}, time.Time{}, nil, fmt.Errorf("readiness request repeats required check %q", requirement.CheckID)
		}
		required[requirement.CheckID] = requirement.CheckVersion
	}
	aggregateObservedAt, err := time.Parse(time.RFC3339Nano, output.ObservedAt)
	if err != nil {
		return time.Time{}, time.Time{}, nil, errors.New("readiness observation has an invalid timestamp")
	}
	if aggregateObservedAt.After(now) || !aggregateObservedAt.Add(ttl).After(now) {
		return time.Time{}, time.Time{}, nil, errors.New("readiness observation is stale or from the future")
	}
	expiresAt := aggregateObservedAt.Add(ttl)
	checkVersions := make([]string, 0, len(output.CheckResults))
	for _, check := range output.CheckResults {
		expectedVersion, ok := required[check.CheckID]
		if !ok {
			return time.Time{}, time.Time{}, nil, fmt.Errorf("readiness observation contains unrequested check %q", check.CheckID)
		}
		delete(required, check.CheckID)
		if check.CheckVersion == "" || check.CheckVersion != expectedVersion || check.Result != "pass" {
			return time.Time{}, time.Time{}, nil, fmt.Errorf("required readiness check %q did not pass at its declared version", check.CheckID)
		}
		observedAt, parseErr := time.Parse(time.RFC3339Nano, check.ObservedAt)
		if parseErr != nil || observedAt.After(aggregateObservedAt) || observedAt.After(now) || !observedAt.Add(ttl).After(now) {
			return time.Time{}, time.Time{}, nil, fmt.Errorf("readiness check %q has an invalid, stale, or future observation time", check.CheckID)
		}
		if checkExpiresAt := observedAt.Add(ttl); checkExpiresAt.Before(expiresAt) {
			expiresAt = checkExpiresAt
		}
		checkVersions = append(checkVersions, check.CheckID+"@"+check.CheckVersion)
	}
	if len(required) != 0 {
		return time.Time{}, time.Time{}, nil, errors.New("readiness observation omitted one or more required checks")
	}
	sort.Strings(checkVersions)
	return aggregateObservedAt, expiresAt, checkVersions, nil
}

func joinCheckVersions(values []string) string {
	var joined string
	for _, value := range values {
		joined += fmt.Sprintf("%d:%s", len(value), value)
	}
	return joined
}
