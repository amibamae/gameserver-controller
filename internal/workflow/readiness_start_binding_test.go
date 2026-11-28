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

// startReadinessCase is one server.start completion with the given readiness
// evidence and the exact error it must produce, or none.
type startReadinessCase struct {
	name     string
	evidence startStopR3Evidence
	want     string
}

// runStartReadinessCases checks that completion validation and the success
// result both give each case's exact outcome.
func runStartReadinessCases(t *testing.T, cases []startReadinessCase) {
	t.Helper()
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

// startReadinessTimes is fresh evidence for one runtime with readiness
// dispatched at dispatchedAt and observed at the given times.
func startReadinessTimes(dispatchedAt, observedAt, checkObservedAt time.Time) startStopR3Evidence {
	evidence := startStopR3FreshEvidence()
	evidence.readinessDispatchedAt, evidence.observedAt, evidence.checkObservedAt = dispatchedAt, observedAt, checkObservedAt
	return evidence
}

// TestStartReadinessMustFollowItsDispatch pins that server.start readiness
// evidence, in aggregate and for every check, was evaluated no earlier than
// the readiness step was dispatched, less the 30s clock-skew allowance. The
// readiness step is first dispatched only after the final unit start attempt
// succeeded, so this also rejects evidence from before that start.
func TestStartReadinessMustFollowItsDispatch(t *testing.T) {
	const aggregateEarly = "readiness observation predates its dispatch by more than the 30s clock-skew allowance"
	const checkEarly = `readiness check "paper.startup-complete" observation predates its dispatch by more than the 30s clock-skew allowance`
	dispatchedAt := time.Now().UTC().Add(-time.Second)
	allowed := dispatchedAt.Add(-30 * time.Second)
	tooEarly := allowed.Add(-time.Millisecond)
	runStartReadinessCases(t, []startReadinessCase{
		{name: "aggregate and checks 1ms beyond the skew allowance", evidence: startReadinessTimes(dispatchedAt, tooEarly, tooEarly), want: aggregateEarly},
		{name: "checks 1ms beyond the skew allowance", evidence: startReadinessTimes(dispatchedAt, dispatchedAt, tooEarly), want: checkEarly},
		{name: "observed at the skew allowance", evidence: startReadinessTimes(dispatchedAt, allowed, allowed)},
		{name: "observed at dispatch", evidence: startReadinessTimes(dispatchedAt, dispatchedAt, dispatchedAt)},
		{name: "observed 1ms after dispatch", evidence: startReadinessTimes(dispatchedAt, dispatchedAt.Add(time.Millisecond), dispatchedAt.Add(time.Millisecond))},
	})
}

// startReadinessRuntimes is fresh evidence whose unit inspection and
// readiness observation report the given runtime IDs.
func startReadinessRuntimes(inspected, observed string) startStopR3Evidence {
	evidence := startStopR3FreshEvidence()
	evidence.inspectRuntimeID, evidence.readinessRuntimeID = inspected, observed
	return evidence
}

// TestStartReadinessMustNameTheStartedRuntime pins that server.start
// readiness is bound to the runtime incarnation the post-start unit
// inspection reported, and fails closed when either side omits it.
func TestStartReadinessMustNameTheStartedRuntime(t *testing.T) {
	runStartReadinessCases(t, []startReadinessCase{
		{name: "same runtime", evidence: startReadinessRuntimes(startStopR3RuntimeID, startStopR3RuntimeID)},
		{name: "readiness from another runtime", evidence: startReadinessRuntimes(startStopR3RuntimeID, "paper-runtime-r3-000"),
			want: "readiness observation runtime_id does not match the started runtime"},
		{name: "readiness without runtime", evidence: startReadinessRuntimes(startStopR3RuntimeID, ""),
			want: "readiness observation did not report a runtime_id"},
		{name: "inspection without runtime", evidence: startReadinessRuntimes("", startStopR3RuntimeID),
			want: "unit inspection after start did not report a runtime_id"},
	})
}

// TestStartReadinessRequiresRecordedDispatch pins that a readiness step
// without a stored dispatch time cannot anchor its evidence.
func TestStartReadinessRequiresRecordedDispatch(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3Ready(t, f)
	startStopR3SetStep(&operation, startReadinessStep, func(step *store.OperationStep) { step.StartedAt = nil })
	const want = "readiness step has no recorded dispatch time"
	if got := startStopR3ErrText(f.start.ValidateCompletion(context.Background(), operation)); got != want {
		t.Fatalf("ValidateCompletion = %q; want %q", got, want)
	}
}

// TestCheckReadinessFollowsStartFailsClosed pins the branches the start
// workflow cannot reach after schema validation: undecodable output and an
// unparseable observation time.
func TestCheckReadinessFollowsStartFailsClosed(t *testing.T) {
	notBefore := time.Now().UTC()
	start := startedRuntime{notBefore: notBefore, runtimeID: startStopR3RuntimeID}
	cases := map[string]string{
		`[]`: "decode readiness observation",
		`{"observed_at":"not-a-time","check_results":[]}`: "readiness observation predates its dispatch by more than the 30s clock-skew allowance",
		`{"observed_at":"` + notBefore.Format(time.RFC3339Nano) + `","check_results":[{"check_id":"paper.world-loaded","observed_at":"x"}]}`: `readiness check "paper.world-loaded" observation predates its dispatch by more than the 30s clock-skew allowance`,
	}
	for output, want := range cases {
		if got := startStopR3ErrText(checkReadinessFollowsStart(json.RawMessage(output), start)); got != want {
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
