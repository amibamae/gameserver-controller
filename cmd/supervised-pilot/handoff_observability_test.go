package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

func autoLogTestOptions() handoffOptions {
	return handoffOptions{
		Execute: true, AutoVictoriaLogs: true, Confirmation: "vanilla@staging/instance-1",
		OperationID: "0123456789abcdef0123456789abcdef",
		Timeout: time.Hour, CredentialRef: defaultVultrCredential,
		SSHCredentialRef: defaultSSHCredential,
		LogsCredentialRef: "systemd-credential://gsc-victorialogs-ingest-token",
		LogsPrivacyMode: "lifecycle", LogsEndpoint: "https://logs.example/insert/jsonline",
	}
}

func TestAutomaticObservabilityOptOutDoesNotCallProvider(t *testing.T) {
	opts := autoLogTestOptions()
	opts.AutoVictoriaLogs = false
	calls := 0
	apply := func(observabilityOptions, string, io.Writer) error {
		calls++
		return errors.New("must not be called")
	}
	applyAutomaticObservability(opts, "unused.db", io.Discard, io.Discard, apply)
	opts.AutoVictoriaLogs = true
	opts.Execute = false
	applyAutomaticObservability(opts, "unused.db", io.Discard, io.Discard, apply)
	if calls != 0 {
		t.Fatalf("disabled and dry-run handoffs invoked observability %d times", calls)
	}
}

func TestAutomaticObservabilityFailureDoesNotFailHandoff(t *testing.T) {
	opts := autoLogTestOptions()
	var stdout, stderr bytes.Buffer
	calls := 0
	applyAutomaticObservability(opts, "test.db", &stdout, &stderr, func(o observabilityOptions, path string, out io.Writer) error {
		calls++
		if !o.Execute || o.OperationID != opts.OperationID || o.Confirmation != opts.Confirmation || path != "test.db" || o.Timeout != 2*time.Minute {
			t.Fatalf("unexpected automatic configuration: %+v, %s", o, path)
		}
		return errors.New("private-token-secret-that-must-not-be-logged")
	})
	if calls != 1 || !strings.Contains(stderr.String(), "optional VictoriaLogs setup failed") {
		t.Fatalf("calls %d, warning %q", calls, stderr.String())
	}
	if strings.Contains(stderr.String(), "private-token-secret") {
		t.Fatalf("secret appeared in warning %q", stderr.String())
	}
}

func TestAutomaticObservabilityRequiresExplicitOptIn(t *testing.T) {
	options := autoLogTestOptions()
	flags := flag.NewFlagSet("handoff", flag.ContinueOnError)
	flags.String("logs-endpoint", "", "")
	if err := flags.Set("logs-endpoint", options.LogsEndpoint); err != nil {
		t.Fatal(err)
	}
	options.AutoVictoriaLogs = false
	if err := validateAutoObservabilityFlags(flags, options); err == nil || err.Error() != "VictoriaLogs handoff options require --enable-victorialogs" {
		t.Fatalf("unexpected opt-out validation: %v", err)
	}
	options.AutoVictoriaLogs = true
	options.LogsPrivacyMode = "raw"
	if err := validateAutoObservabilityFlags(flags, options); err == nil || err.Error() != "automatic VictoriaLogs handoff supports lifecycle privacy only" {
		t.Fatalf("unexpected raw-log validation: %v", err)
	}
	options.LogsPrivacyMode = "lifecycle"
	options.LogsEndpoint = "http://logs.example/insert/jsonline"
	if err := validateAutoObservabilityFlags(flags, options); err == nil || err.Error() != "automatic VictoriaLogs endpoint must be HTTPS /insert/jsonline" {
		t.Fatalf("unexpected HTTP validation: %v", err)
	}
	options.LogsEndpoint = "https://logs.example/insert/jsonline"
	if err := validateAutoObservabilityFlags(flags, options); err != nil {
		t.Fatalf("safe opt-in rejected: %v", err)
	}
}
