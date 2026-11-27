package velocityproxy

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

type velocityR6ActionCase struct {
	capability string
	input      any
	setup      func(*velocityR6Harness)
	want       velocityR6Want
}

const (
	velocityR6Register   = "integration.proxy.backend.register"
	velocityR6Drain      = "integration.proxy.backend.drain"
	velocityR6Unregister = "integration.proxy.backend.unregister"
)

func velocityR6RegisterInput(expires time.Time) registerInput {
	return registerInput{Target: target, ReadinessAssertion: readiness(expires)}
}

// velocityR6Routable stores paper-main as registered and routable to the
// target instance, observed at velocityR6SeenAt.
func velocityR6Routable(h *velocityR6Harness) {
	h.bridge.backends["paper-main"] = velocityR6Backend(Registered, velocityR6Ref(instance), true, nil, velocityR6SeenAt)
}

// velocityR6NotAttempted wraps ErrNotAttempted, so errors.Is must unwrap it.
var velocityR6NotAttempted = fmt.Errorf("velocityR6 bridge refused: %w", ErrNotAttempted)

func velocityR6RegisterActionCases() map[string]velocityR6ActionCase {
	valid := velocityR6RegisterInput(testNow.Add(time.Minute))
	register := "register paper-main " + backendAddress
	return map[string]velocityR6ActionCase{
		"readiness expired reports observed state": {velocityR6Register, velocityR6RegisterInput(testNow), velocityR6Routable,
			velocityR6Want{velocityR6ActionJSON("rejected", "registered", "routable", velocityR6Seen, "readiness_assertion_expired"), adapter.OutcomeCompleted,
				[]string{"inspect paper-main", "resolve paper-main"}}},
		"readiness mismatch without observation": {velocityR6Register, registerInput{Target: target, ReadinessAssertion: func() ReadinessAssertion {
			a := readiness(testNow.Add(time.Minute))
			a.ServerID = "other"
			return a
		}()}, func(h *velocityR6Harness) { h.bridge.inspectFails = []bool{true} },
			velocityR6Want{velocityR6ActionJSON("rejected", "unknown", "unknown", velocityR6Now, "readiness_assertion_mismatch"), adapter.OutcomeCompleted,
				[]string{"inspect paper-main"}}},
		"address unavailable": {velocityR6Register, valid, func(h *velocityR6Harness) { h.resolver.err = errors.New("velocityR6 no address") },
			velocityR6Want{velocityR6ActionJSON("rejected", "unregistered", "not_routable", velocityR6Now, "backend_address_unavailable"), adapter.OutcomeCompleted,
				[]string{"resolve paper-main", "inspect paper-main"}}},
		"not attempted": {velocityR6Register, valid, func(h *velocityR6Harness) { h.bridge.actionErr = velocityR6NotAttempted },
			velocityR6Want{velocityR6ActionJSON("rejected", "unregistered", "not_routable", velocityR6Now, "proxy_action_not_attempted"), adapter.OutcomeCompleted,
				[]string{"resolve paper-main", register, "inspect paper-main"}}},
		"uncertain": {velocityR6Register, valid, func(h *velocityR6Harness) { h.bridge.actionErr = errors.New("velocityR6 connection reset") },
			velocityR6Want{velocityR6ActionJSON("outcome_unknown", "unknown", "unknown", velocityR6Now, "proxy_action_outcome_unknown"), adapter.OutcomeUnknown,
				[]string{"resolve paper-main", register}}},
		"confirmed": {velocityR6Register, valid, func(*velocityR6Harness) {},
			velocityR6Want{velocityR6ActionJSON("confirmed", "registered", "routable", velocityR6Now, ""), adapter.OutcomeCompleted,
				[]string{"resolve paper-main", register, "inspect paper-main", "resolve paper-main"}}},
		"confirmation unavailable": {velocityR6Register, valid, func(h *velocityR6Harness) { h.bridge.inspectFails = []bool{true} },
			velocityR6Want{velocityR6ActionJSON("accepted", "unknown", "unknown", velocityR6Now, "confirmation_unavailable"), adapter.OutcomeAccepted,
				[]string{"resolve paper-main", register, "inspect paper-main"}}},
		"registered but not routable": {velocityR6Register, valid, func(h *velocityR6Harness) {
			h.bridge.actionApplies = false
			h.bridge.backends["paper-main"] = velocityR6Backend(Registered, velocityR6Ref(instance), false, nil, velocityR6SeenAt)
		}, velocityR6Want{velocityR6ActionJSON("accepted", "registered", "not_routable", velocityR6Seen, "postcondition_not_observed"), adapter.OutcomeAccepted,
			[]string{"resolve paper-main", register, "inspect paper-main"}}},
	}
}

func velocityR6ChangeActionCases() map[string]velocityR6ActionCase {
	input := backendInput{Target: target}
	notApplied := func(h *velocityR6Harness) {
		velocityR6Routable(h)
		h.bridge.actionApplies = false
	}
	return map[string]velocityR6ActionCase{
		"drain confirmed": {velocityR6Drain, input, velocityR6Routable,
			velocityR6Want{velocityR6ActionJSON("confirmed", "draining", "not_routable", velocityR6Now, ""), adapter.OutcomeCompleted,
				[]string{"drain paper-main", "inspect paper-main"}}},
		"drain not applied hides routable": {velocityR6Drain, input, notApplied,
			velocityR6Want{velocityR6ActionJSON("accepted", "registered", "unknown", velocityR6Seen, "postcondition_not_observed"), adapter.OutcomeAccepted,
				[]string{"drain paper-main", "inspect paper-main", "resolve paper-main"}}},
		"drain uncertain": {velocityR6Drain, input, func(h *velocityR6Harness) { h.bridge.actionErr = errors.New("velocityR6 timeout") },
			velocityR6Want{velocityR6ActionJSON("outcome_unknown", "unknown", "unknown", velocityR6Now, "proxy_action_outcome_unknown"), adapter.OutcomeUnknown,
				[]string{"drain paper-main"}}},
		"unregister confirmed": {velocityR6Unregister, input, velocityR6Routable,
			velocityR6Want{velocityR6ActionJSON("confirmed", "unregistered", "not_routable", velocityR6Now, ""), adapter.OutcomeCompleted,
				[]string{"unregister paper-main", "inspect paper-main"}}},
		"unregister still draining": {velocityR6Unregister, input, func(h *velocityR6Harness) {
			h.bridge.actionApplies = false
			h.bridge.backends["paper-main"] = velocityR6Backend(Draining, velocityR6Ref(instance), false, nil, velocityR6SeenAt)
		}, velocityR6Want{velocityR6ActionJSON("accepted", "draining", "not_routable", velocityR6Seen, "postcondition_not_observed"), adapter.OutcomeAccepted,
			[]string{"unregister paper-main", "inspect paper-main"}}},
		"unregister not attempted without observation": {velocityR6Unregister, input, func(h *velocityR6Harness) {
			h.bridge.actionErr = velocityR6NotAttempted
			h.bridge.inspectFails = []bool{true}
		}, velocityR6Want{velocityR6ActionJSON("rejected", "unknown", "unknown", velocityR6Now, "proxy_action_not_attempted"), adapter.OutcomeCompleted,
			[]string{"unregister paper-main", "inspect paper-main"}}},
		"unregister not attempted reports observed state": {velocityR6Unregister, input, func(h *velocityR6Harness) {
			velocityR6Routable(h)
			h.bridge.actionErr = velocityR6NotAttempted
		}, velocityR6Want{velocityR6ActionJSON("rejected", "registered", "routable", velocityR6Seen, "proxy_action_not_attempted"), adapter.OutcomeCompleted,
			[]string{"unregister paper-main", "inspect paper-main", "resolve paper-main"}}},
	}
}

// TestVelocityR6BackendActionOutputs pins every branch of registerBackend,
// changeBackend, rejected, unobservedAction, and applyAndConfirm: the encoded
// output, the uncertain-outcome mapping, and the call order.
func TestVelocityR6BackendActionOutputs(t *testing.T) {
	for _, cases := range []map[string]velocityR6ActionCase{velocityR6RegisterActionCases(), velocityR6ChangeActionCases()} {
		for name, test := range cases {
			t.Run(name, func(t *testing.T) {
				h := newVelocityR6Harness(t)
				test.setup(h)
				output, outcome := h.invoke(test.capability, test.input)
				h.expect(output, outcome, test.want)
			})
		}
	}
	if !errors.Is(velocityR6NotAttempted, ErrNotAttempted) || ErrNotAttempted.Error() != "proxy action was not attempted" {
		t.Fatalf("ErrNotAttempted = %q", ErrNotAttempted)
	}
}
