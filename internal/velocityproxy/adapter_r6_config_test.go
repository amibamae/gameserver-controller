package velocityproxy

import (
	"strings"
	"testing"
	"time"
)

const (
	velocityR6IdentityError = "an integration adapter ID and environment are required"
	velocityR6ProxyRefError = "proxy instance reference must name this adapter, its environment, and a proxy-instance ID"
	velocityR6DepsError     = "protocol version, proxy bridge, and backend address resolver are required"
)

// velocityR6ConfigWith returns the harness config after one change.
func velocityR6ConfigWith(change func(*Config)) Config {
	config := velocityR6Config()
	change(&config)
	return config
}

// TestVelocityR6NewAdapterErrors pins every branch of newAdapter and
// validateConfig, including boundaries and the order of the checks.
func TestVelocityR6NewAdapterErrors(t *testing.T) {
	longEnvironment := strings.Repeat("e", 200)
	cases := map[string]struct {
		config Config
		want   string
	}{
		"valid": {velocityR6Config(), ""},
		"non-integration ID": {velocityR6ConfigWith(func(c *Config) {
			c.AdapterID, c.ProxyInstanceRef.AdapterID = "hosting.velocity", "hosting.velocity"
		}), velocityR6IdentityError},
		"bare integration prefix": {velocityR6ConfigWith(func(c *Config) { c.AdapterID = "integration." }), velocityR6IdentityError},
		"missing environment":     {velocityR6ConfigWith(func(c *Config) { c.EnvironmentID = "" }), velocityR6IdentityError},
		"longest environment": {velocityR6ConfigWith(func(c *Config) {
			c.EnvironmentID, c.ProxyInstanceRef.EnvironmentID = longEnvironment, longEnvironment
		}), ""},
		"environment too long": {velocityR6ConfigWith(func(c *Config) {
			c.EnvironmentID, c.ProxyInstanceRef.EnvironmentID = longEnvironment+"e", longEnvironment+"e"
		}), velocityR6IdentityError},
		"identity checked first": {velocityR6ConfigWith(func(c *Config) { c.EnvironmentID, c.ProxyInstanceRef.ExternalID = "", "" }), velocityR6IdentityError},
		"proxy ref other owner":  {velocityR6ConfigWith(func(c *Config) { c.ProxyInstanceRef.AdapterID = "integration.other" }), velocityR6ProxyRefError},
		"proxy ref other env":    {velocityR6ConfigWith(func(c *Config) { c.ProxyInstanceRef.EnvironmentID = "prod" }), velocityR6ProxyRefError},
		"proxy ref wrong kind":   {velocityR6ConfigWith(func(c *Config) { c.ProxyInstanceRef.ResourceKind = "instance" }), velocityR6ProxyRefError},
		"proxy ref no name":      {velocityR6ConfigWith(func(c *Config) { c.ProxyInstanceRef.ExternalID = "" }), velocityR6ProxyRefError},
		"proxy ref longest name": {velocityR6ConfigWith(func(c *Config) { c.ProxyInstanceRef.ExternalID = strings.Repeat("x", 1024) }), ""},
		"proxy ref name too long": {velocityR6ConfigWith(func(c *Config) {
			c.ProxyInstanceRef.ExternalID = strings.Repeat("x", 1025)
		}), velocityR6ProxyRefError},
	}
	h := newVelocityR6Harness(t)
	now := func() time.Time { return testNow }
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := newAdapter(test.config, "0.21.0-draft", h.bridge, h.resolver, now)
			velocityR6ExpectError(t, err, test.want)
			if (err == nil) != (got != nil) {
				t.Fatalf("adapter = %v with error %v", got, err)
			}
		})
	}
	dependencies := map[string]func() (*Adapter, error){
		"no protocol version": func() (*Adapter, error) { return newAdapter(velocityR6Config(), "", h.bridge, h.resolver, now) },
		"no bridge":           func() (*Adapter, error) { return newAdapter(velocityR6Config(), "0.21.0-draft", nil, h.resolver, now) },
		"no resolver":         func() (*Adapter, error) { return newAdapter(velocityR6Config(), "0.21.0-draft", h.bridge, nil, now) },
		"no clock": func() (*Adapter, error) {
			return newAdapter(velocityR6Config(), "0.21.0-draft", h.bridge, h.resolver, nil)
		},
	}
	for name, build := range dependencies {
		got, err := build()
		if got != nil {
			t.Errorf("%s: adapter was built", name)
		}
		velocityR6ExpectError(t, err, velocityR6DepsError)
	}
	// The config is checked before the dependencies.
	_, err := New(Config{}, "", nil, nil)
	velocityR6ExpectError(t, err, velocityR6IdentityError)
}

func velocityR6ExpectError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("error = %v, want none", err)
		}
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// TestVelocityR6ReadinessProblem pins every branch of readinessProblem and
// the precedence of its reason codes.
func TestVelocityR6ReadinessProblem(t *testing.T) {
	h := newVelocityR6Harness(t)
	with := func(change func(*ReadinessAssertion)) ReadinessAssertion {
		assertion := readiness(testNow.Add(time.Minute))
		change(&assertion)
		return assertion
	}
	cases := map[string]struct {
		assertion ReadinessAssertion
		want      string
	}{
		"current":             {with(func(*ReadinessAssertion) {}), ""},
		"observed now":        {with(func(a *ReadinessAssertion) { a.ObservedAt = testNow }), ""},
		"other server":        {with(func(a *ReadinessAssertion) { a.ServerID = "other" }), "readiness_assertion_mismatch"},
		"other environment":   {with(func(a *ReadinessAssertion) { a.EnvironmentID = "prod" }), "readiness_assertion_mismatch"},
		"other instance":      {with(func(a *ReadinessAssertion) { a.InstanceRef = otherInstance }), "readiness_assertion_mismatch"},
		"mismatch first":      {with(func(a *ReadinessAssertion) { a.ServerID, a.ExpiresAt = "other", time.Time{} }), "readiness_assertion_mismatch"},
		"no observed_at":      {with(func(a *ReadinessAssertion) { a.ObservedAt = time.Time{} }), "readiness_assertion_invalid"},
		"no expires_at":       {with(func(a *ReadinessAssertion) { a.ExpiresAt = time.Time{} }), "readiness_assertion_invalid"},
		"empty window":        {with(func(a *ReadinessAssertion) { a.ExpiresAt = a.ObservedAt }), "readiness_assertion_invalid"},
		"reversed window":     {with(func(a *ReadinessAssertion) { a.ExpiresAt = a.ObservedAt.Add(-time.Second) }), "readiness_assertion_invalid"},
		"invalid before time": {with(func(a *ReadinessAssertion) { a.ObservedAt, a.ExpiresAt = testNow.Add(time.Hour), testNow }), "readiness_assertion_invalid"},
		"not yet valid": {with(func(a *ReadinessAssertion) {
			a.ObservedAt, a.ExpiresAt = testNow.Add(time.Second), testNow.Add(time.Hour)
		}), "readiness_assertion_not_yet_valid"},
		"expires now": {with(func(a *ReadinessAssertion) { a.ExpiresAt = testNow }), "readiness_assertion_expired"},
		"expired":     {with(func(a *ReadinessAssertion) { a.ExpiresAt = testNow.Add(-time.Second) }), "readiness_assertion_expired"},
		"expired early": {with(func(a *ReadinessAssertion) {
			a.ObservedAt, a.ExpiresAt = testNow.Add(-time.Hour), testNow.Add(-time.Minute)
		}), "readiness_assertion_expired"},
	}
	for name, test := range cases {
		if got := h.adapter.readinessProblem(test.assertion, "minecraft-test", "test", instance); got != test.want {
			t.Errorf("%s: readinessProblem = %q, want %q", name, got, test.want)
		}
	}
}
