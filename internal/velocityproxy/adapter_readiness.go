package velocityproxy

// readinessProblem returns a reason code when the assertion cannot authorize
// routing to the exact target, or "" when it is current and bound.
func (a *Adapter) readinessProblem(assertion ReadinessAssertion, serverID, environmentID string, instance ResourceRef) string {
	if !readinessBound(assertion, serverID, environmentID, instance) {
		return "readiness_assertion_mismatch"
	}
	now := a.now()
	if !validReadinessWindow(assertion) {
		return "readiness_assertion_invalid"
	}
	if assertion.ObservedAt.After(now) {
		return "readiness_assertion_not_yet_valid"
	}
	if !assertion.ExpiresAt.After(now) {
		return "readiness_assertion_expired"
	}
	return ""
}

// readinessBound reports whether the assertion names the exact server,
// environment, and instance.
func readinessBound(assertion ReadinessAssertion, serverID, environmentID string, instance ResourceRef) bool {
	return assertion.ServerID == serverID && assertion.EnvironmentID == environmentID && assertion.InstanceRef == instance
}

// validReadinessWindow reports whether both validity times are set and the
// assertion expires after it was observed.
func validReadinessWindow(assertion ReadinessAssertion) bool {
	return !assertion.ObservedAt.IsZero() && !assertion.ExpiresAt.IsZero() && assertion.ExpiresAt.After(assertion.ObservedAt)
}
