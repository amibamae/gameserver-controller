package velocityproxy

import (
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

type velocityR6InspectCase struct {
	observed    *BackendObservation
	inspectFail bool
	resolveErr  error
	resolveTo   string
	want        velocityR6Want
}

func (test velocityR6InspectCase) setup(h *velocityR6Harness) {
	if test.observed != nil {
		h.bridge.backends["paper-main"] = *test.observed
	}
	if test.inspectFail {
		h.bridge.inspectFails = []bool{true}
	}
	h.resolver.err = test.resolveErr
	if test.resolveTo != "" {
		h.resolver.address = test.resolveTo
	}
}

func velocityR6Observation(observed BackendObservation) *BackendObservation { return &observed }

// TestVelocityR6InspectBackendOutputs pins every branch of inspectBackend and
// boundState as encoded output, outcome, and bridge and resolver calls.
func TestVelocityR6InspectBackendOutputs(t *testing.T) {
	inspect := []string{"inspect paper-main"}
	resolved := []string{"inspect paper-main", "resolve paper-main"}
	done := adapter.OutcomeCompleted
	mine := velocityR6Ref(instance)
	cases := map[string]velocityR6InspectCase{
		"bridge error": {inspectFail: true,
			want: velocityR6Want{velocityR6StateJSON("unknown", "unknown", "", velocityR6Now), done, inspect}},
		"routable with count": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(4), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("registered", "routable", "4", velocityR6Seen), done, resolved}},
		"routable zero count": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(0), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("registered", "routable", "0", velocityR6Seen), done, resolved}},
		"routable negative count": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(-1), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("registered", "routable", "", velocityR6Seen), done, resolved}},
		"routable without count": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, nil, velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("registered", "routable", "", velocityR6Seen), done, resolved}},
		"routable zero observed_at": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, nil, time.Time{})),
			want: velocityR6Want{velocityR6StateJSON("registered", "routable", "", velocityR6Now), done, resolved}},
		"registered not routable": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, false, velocityR6Count(3), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("registered", "not_routable", "3", velocityR6Seen), done, inspect}},
		"draining keeps count": {observed: velocityR6Observation(velocityR6Backend(Draining, mine, true, velocityR6Count(2), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("draining", "not_routable", "2", velocityR6Seen), done, inspect}},
		"draining other instance": {observed: velocityR6Observation(velocityR6Backend(Draining, velocityR6Ref(otherInstance), false, velocityR6Count(2), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("unknown", "not_routable", "", velocityR6Seen), done, inspect}},
		"unregistered drops count": {observed: velocityR6Observation(velocityR6Backend(Unregistered, mine, false, velocityR6Count(5), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("unregistered", "not_routable", "", velocityR6Seen), done, inspect}},
		"other instance": {observed: velocityR6Observation(velocityR6Backend(Registered, velocityR6Ref(otherInstance), true, velocityR6Count(4), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("unknown", "not_routable", "", velocityR6Seen), done, inspect}},
		"no instance": {observed: velocityR6Observation(velocityR6Backend(Registered, nil, true, velocityR6Count(4), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("unknown", "not_routable", "", velocityR6Seen), done, inspect}},
		"address unresolved": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(4), velocityR6SeenAt)), resolveErr: errors.New("velocityR6 no address"),
			want: velocityR6Want{velocityR6StateJSON("unknown", "unknown", "", velocityR6Seen), done, resolved}},
		"address changed": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(4), velocityR6SeenAt)), resolveTo: "203.0.113.99:25565",
			want: velocityR6Want{velocityR6StateJSON("unknown", "not_routable", "", velocityR6Seen), done, resolved}},
		"proxy unknown": {observed: velocityR6Observation(velocityR6Backend(Unknown, mine, true, velocityR6Count(4), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6StateJSON("unknown", "unknown", "", velocityR6Seen), done, inspect}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newVelocityR6Harness(t)
			test.setup(h)
			output, outcome := h.invoke("integration.proxy.backend.inspect", backendInput{Target: target})
			h.expect(output, outcome, test.want)
		})
	}
}

// TestVelocityR6InspectPlayersOutputs pins every branch of inspectPlayers,
// including that the address is resolved only once a count is present.
func TestVelocityR6InspectPlayersOutputs(t *testing.T) {
	inspect := []string{"inspect paper-main"}
	resolved := []string{"inspect paper-main", "resolve paper-main"}
	done := adapter.OutcomeCompleted
	mine := velocityR6Ref(instance)
	unknown := velocityR6PlayersJSON(-1, "")
	at := func(offset time.Duration) time.Time { return testNow.Add(offset) }
	cases := map[string]velocityR6InspectCase{
		"bridge error": {inspectFail: true, want: velocityR6Want{unknown, done, inspect}},
		"no count": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, nil, velocityR6SeenAt)),
			want: velocityR6Want{unknown, done, inspect}},
		"negative count": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(-1), velocityR6SeenAt)),
			want: velocityR6Want{unknown, done, inspect}},
		"zero observed_at": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(3), time.Time{})),
			want: velocityR6Want{unknown, done, inspect}},
		"other instance": {observed: velocityR6Observation(velocityR6Backend(Registered, velocityR6Ref(otherInstance), true, velocityR6Count(3), velocityR6SeenAt)),
			want: velocityR6Want{unknown, done, inspect}},
		"unregistered": {observed: velocityR6Observation(velocityR6Backend(Unregistered, mine, false, velocityR6Count(3), velocityR6SeenAt)),
			want: velocityR6Want{unknown, done, inspect}},
		"proxy unknown": {observed: velocityR6Observation(velocityR6Backend(Unknown, mine, false, velocityR6Count(3), velocityR6SeenAt)),
			want: velocityR6Want{unknown, done, inspect}},
		"address unresolved": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(3), velocityR6SeenAt)), resolveErr: errors.New("velocityR6 no address"),
			want: velocityR6Want{unknown, done, resolved}},
		"stale": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(3), at(-31*time.Second))),
			want: velocityR6Want{unknown, done, resolved}},
		"future": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(3), at(time.Second))),
			want: velocityR6Want{unknown, done, resolved}},
		"oldest allowed": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(3), at(-30*time.Second))),
			want: velocityR6Want{velocityR6PlayersJSON(3, "2026-11-07T09:59:30Z"), done, resolved}},
		"routable": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, true, velocityR6Count(3), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6PlayersJSON(3, velocityR6Seen), done, resolved}},
		"registered not routable": {observed: velocityR6Observation(velocityR6Backend(Registered, mine, false, velocityR6Count(0), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6PlayersJSON(0, velocityR6Seen), done, inspect}},
		"draining": {observed: velocityR6Observation(velocityR6Backend(Draining, mine, false, velocityR6Count(7), velocityR6SeenAt)),
			want: velocityR6Want{velocityR6PlayersJSON(7, velocityR6Seen), done, inspect}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newVelocityR6Harness(t)
			test.setup(h)
			output, outcome := h.invoke("integration.proxy.backend.players.inspect", playersInput{Target: target, MaximumAgeSeconds: 30})
			h.expect(output, outcome, test.want)
		})
	}
}
