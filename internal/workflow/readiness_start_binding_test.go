package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// TestStartReadinessMustFollowUnitStart pins that server.start readiness
// evidence, in aggregate and for every check, is no earlier than the
// completion of this operation's unit start.
func TestStartReadinessMustFollowUnitStart(t *testing.T) {
	const aggregateEarly = "readiness observation predates this operation's unit start"
	const checkEarly = `readiness check "paper.startup-complete" observation predates this operation's unit start`
	completedAt := time.Now().UTC().Add(-time.Second)
	before := completedAt.Add(-time.Millisecond)
	after := completedAt.Add(time.Millisecond)
	cases := []struct {
		name     string
		evidence startStopR3Evidence
		want     string
	}{
		{name: "aggregate and checks 1ms before start completion",
			evidence: startStopR3Evidence{startCompletedAt: completedAt, observedAt: before, checkObservedAt: before}, want: aggregateEarly},
		{name: "checks 1ms before start completion",
			evidence: startStopR3Evidence{startCompletedAt: completedAt, observedAt: completedAt, checkObservedAt: before}, want: checkEarly},
		{name: "observed at start completion",
			evidence: startStopR3Evidence{startCompletedAt: completedAt, observedAt: completedAt, checkObservedAt: completedAt}},
		{name: "observed 1ms after start completion",
			evidence: startStopR3Evidence{startCompletedAt: completedAt, observedAt: after, checkObservedAt: after}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3ReadyWith(t, f, test.evidence)
			if got := startStopR3ErrText(f.start.ValidateCompletion(context.Background(), operation)); got != test.want {
				t.Fatalf("ValidateCompletion = %q; want %q", got, test.want)
			}
			if _, err := f.start.BuildSuccessResult(context.Background(), operation); startStopR3ErrText(err) != test.want {
				t.Fatalf("BuildSuccessResult error = %q; want %q", startStopR3ErrText(err), test.want)
			}
		})
	}
}

// TestStartReadinessRequiresRecordedStartCompletion pins that a start step
// without a stored completion time cannot anchor readiness.
func TestStartReadinessRequiresRecordedStartCompletion(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3Ready(t, f)
	startStopR3SetStep(&operation, startRuntimeStep, func(step *store.OperationStep) { step.CompletedAt = nil })
	const want = "unit start step has no recorded completion time"
	if got := startStopR3ErrText(f.start.ValidateCompletion(context.Background(), operation)); got != want {
		t.Fatalf("ValidateCompletion = %q; want %q", got, want)
	}
}

// TestCheckReadinessFollowsStartFailsClosed pins the branches the start
// workflow cannot reach after schema validation: undecodable output and an
// unparseable observation time.
func TestCheckReadinessFollowsStartFailsClosed(t *testing.T) {
	completedAt := time.Now().UTC()
	cases := map[string]string{
		`[]`: "decode readiness observation",
		`{"observed_at":"not-a-time","check_results":[]}`: "readiness observation predates this operation's unit start",
		`{"observed_at":"` + completedAt.Format(time.RFC3339Nano) + `","check_results":[{"check_id":"paper.world-loaded","observed_at":"x"}]}`: `readiness check "paper.world-loaded" observation predates this operation's unit start`,
	}
	for output, want := range cases {
		if got := startStopR3ErrText(checkReadinessFollowsStart(json.RawMessage(output), completedAt)); got != want {
			t.Fatalf("checkReadinessFollowsStart(%s) = %q; want %q", output, got, want)
		}
	}
}

// freshReadinessGame is a scripted game adapter whose readiness replies are
// observed when they are returned, as a real game adapter's are, rather than
// when the script was written before the unit start ran.
type freshReadinessGame struct{ *fakeadapter.Scripted }

func (g freshReadinessGame) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	response, err := g.Scripted.Invoke(ctx, request)
	if err != nil || request.Params.Capability != "game.readiness.check" {
		return response, err
	}
	response.Result.Output, err = stampReadinessObservation(response.Result.Output, time.Now().UTC())
	return response, err
}

// stampReadinessObservation sets the observation time of a readiness output
// and of each of its check results to observedAt.
func stampReadinessObservation(output json.RawMessage, observedAt time.Time) (json.RawMessage, error) {
	var observation map[string]any
	if err := json.Unmarshal(output, &observation); err != nil {
		return nil, fmt.Errorf("decode scripted readiness output: %w", err)
	}
	stamp := observedAt.Format(time.RFC3339Nano)
	observation["observed_at"] = stamp
	// A script without a check list keeps none; schema validation of the
	// stamped output then rejects it.
	results, _ := observation["check_results"].([]any)
	for _, result := range results {
		if check, ok := result.(map[string]any); ok {
			check["observed_at"] = stamp
		}
	}
	return json.Marshal(observation)
}
