package config

import (
	"os"
	"slices"
	"testing"
)

func exampleController(t *testing.T) Controller {
	t.Helper()
	file, err := os.Open("../../configs/controller.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	value, err := decodeConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	return value.Controller
}

func TestValidateControllerAcceptsExample(t *testing.T) {
	var problems []string
	validateController(&problems, exampleController(t))
	if len(problems) != 0 {
		t.Fatalf("validateController reported %v for the example configuration", problems)
	}
}

// TestValidateControllerReportsEachRule pins one message per controller rule,
// so a new controller setting adds its case here beside its check.
func TestValidateControllerReportsEachRule(t *testing.T) {
	limit := func(value int) *int { return &value }
	tests := []struct {
		name   string
		mutate func(*Controller)
		want   string
	}{
		{"remote mode", func(c *Controller) { c.Mode = "remote" }, "controller.mode must be local"},
		{"other state store", func(c *Controller) { c.StateStore.Type = "sqlite" }, "controller.state_store.type must be bbolt"},
		{"remote state path", func(c *Controller) { c.StateStore.Path = "s3://bucket/state.db" }, "controller.state_store.path must be a non-empty local path"},
		{"unbounded timeout", func(c *Controller) { c.Defaults.OperationTimeout = "25h" }, "controller.defaults.operation_timeout must be greater than zero and no more than 24h"},
		{"unset retry limit", func(c *Controller) { c.Defaults.RetryLimit = nil }, "controller.defaults.retry_limit must be explicitly set between 0 and 10"},
		{"retry limit too high", func(c *Controller) { c.Defaults.RetryLimit = limit(11) }, "controller.defaults.retry_limit must be explicitly set between 0 and 10"},
		{"audit disabled", func(c *Controller) { c.Audit.Enabled = false }, "controller.audit.enabled must be true"},
		{"remote audit", func(c *Controller) { c.Audit.Destination = "remote" }, "controller.audit.destination must be local"},
		{"management API without listen address", func(c *Controller) {
			c.ManagementAPI.Enabled = true
			c.ManagementAPI.ListenAddress = "localhost"
		}, "controller.management_api.listen_address must be an IP address and TCP port"},
		{"management API without idempotency keys", func(c *Controller) {
			c.ManagementAPI.Enabled = true
			c.IdempotencyKeys = IdempotencyKeys{}
		}, "controller.idempotency_keys is required when the management API is enabled"},
		{"bad monitoring interval", func(c *Controller) { c.Monitoring.Proxy.CheckInterval = "often" }, "controller.monitoring.proxy.check_interval must be a duration"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller := exampleController(t)
			test.mutate(&controller)
			var problems []string
			validateController(&problems, controller)
			if !slices.Contains(problems, test.want) {
				t.Fatalf("validateController problems = %q, want %q", problems, test.want)
			}
		})
	}
}
