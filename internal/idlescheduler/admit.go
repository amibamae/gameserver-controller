package idlescheduler

import (
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/scalingpolicy"
)

// Reasons the scheduler records when it does not request a shutdown. When
// the policy decides, the Outcome carries the scalingpolicy reason instead,
// such as "in_decision_window" or "waiting_for_decision_window".
const (
	ReasonMissingServerID      = "server_id_missing"
	ReasonDuplicateServerID    = "server_id_duplicate"
	ReasonShutdownInFlight     = "shutdown_in_flight"
	ReasonUnknownCreatedAt     = "created_at_unknown"
	ReasonCreatedAtAfterNow    = "created_at_after_now"
	ReasonMissingIdleEvidence  = "idle_evidence_missing"
	ReasonIdleSinceAfterNow    = "idle_since_after_now"
	ReasonInvalidIdleShutdown  = "idle_shutdown_invalid"
	ReasonInvalidScalingPolicy = "scaling_policy_invalid"
	ReasonRequestFailed        = "shutdown_request_failed"
)

// evidenceCheck returns a Reason* constant when the server's evidence is not
// good enough to decide on, or "" when it is.
type evidenceCheck func(server Server, now time.Time) string

// evidenceChecks run in order; the first failure is the recorded reason.
var evidenceChecks = []evidenceCheck{
	func(server Server, _ time.Time) string {
		return reasonIf(server.CreatedAt.IsZero(), ReasonUnknownCreatedAt)
	},
	func(server Server, now time.Time) string {
		return reasonIf(server.CreatedAt.After(now), ReasonCreatedAtAfterNow)
	},
	func(server Server, _ time.Time) string {
		return reasonIf(!server.Idle.Observed, ReasonMissingIdleEvidence)
	},
	func(server Server, now time.Time) string {
		return reasonIf(server.Idle.IdleSince.After(now), ReasonIdleSinceAfterNow)
	},
	func(server Server, _ time.Time) string {
		return reasonIf(server.IdleShutdown <= 0, ReasonInvalidIdleShutdown)
	},
}

func reasonIf(failed bool, reason string) string {
	if failed {
		return reason
	}
	return ""
}

// admit checks the server's evidence and resolves its scaling policy. It
// returns a non-empty reason, and for an invalid policy the error, when the
// scheduler must not decide.
func admit(server Server, now time.Time) (scalingpolicy.Policy, string, error) {
	for _, check := range evidenceChecks {
		if reason := check(server, now); reason != "" {
			return scalingpolicy.Policy{}, reason, nil
		}
	}
	policy, err := scalingpolicy.Resolve(server.Scaling)
	if err != nil {
		return scalingpolicy.Policy{}, ReasonInvalidScalingPolicy, fmt.Errorf("idle scheduler: server %s: %w", server.ID, err)
	}
	return policy, "", nil
}
