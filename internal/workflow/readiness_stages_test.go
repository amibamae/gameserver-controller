package workflow

import (
	"strings"
	"testing"
	"time"
)

// These guards sit behind schema validation in buildReadinessAssertionAt, so
// the stage helpers are tested directly.

func TestRequiredReadinessChecksRejectsEmptyAndRepeatedChecks(t *testing.T) {
	tests := []struct {
		name         string
		requirements []ReadinessRequirement
		want         string
	}{
		{"empty check ID", []ReadinessRequirement{{CheckVersion: "1"}}, "empty required check ID or version"},
		{"empty check version", []ReadinessRequirement{{CheckID: "process"}}, "empty required check ID or version"},
		{"repeated check", []ReadinessRequirement{{"process", "1"}, {"process", "2"}}, `repeats required check "process"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := requiredReadinessChecks(test.requirements); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("requiredReadinessChecks error = %v, want %q", err, test.want)
			}
		})
	}
	required, err := requiredReadinessChecks([]ReadinessRequirement{{"process", "1"}, {"network", "2"}})
	if err != nil || len(required) != 2 || required["process"] != "1" || required["network"] != "2" {
		t.Fatalf("requiredReadinessChecks = %v, %v", required, err)
	}
}

func TestCheckReadinessResults(t *testing.T) {
	now := readinessFixtureNow()
	input := readinessCheckInput{RequiredChecks: []ReadinessRequirement{{"process", "1"}, {"network", "2"}}}
	passing := func() readinessCheckOutput {
		return readinessCheckOutput{ObservedAt: "2026-11-07T09:00:00Z", CheckResults: []readinessCheckResult{
			{CheckID: "process", CheckVersion: "1", Result: "pass", ObservedAt: "2026-11-07T08:59:50Z"},
			{CheckID: "network", CheckVersion: "2", Result: "pass", ObservedAt: "2026-11-07T08:59:59Z"},
		}}
	}

	observedAt, expiresAt, versions, err := checkReadinessResults(input, passing(), time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.November, 7, 9, 0, 0, 0, time.UTC); !observedAt.Equal(want) {
		t.Fatalf("observedAt = %s, want %s", observedAt, want)
	}
	if want := time.Date(2026, time.November, 7, 9, 0, 50, 0, time.UTC); !expiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want the oldest check's expiry %s", expiresAt, want)
	}
	if strings.Join(versions, ",") != "network@2,process@1" {
		t.Fatalf("check versions = %v, want sorted check@version identities", versions)
	}

	tests := []struct {
		name   string
		mutate func(*readinessCheckOutput)
		want   string
	}{
		{"invalid aggregate timestamp", func(o *readinessCheckOutput) { o.ObservedAt = "not-a-time" }, "invalid timestamp"},
		{"check observed after aggregate", func(o *readinessCheckOutput) { o.CheckResults[0].ObservedAt = "2026-11-07T09:00:01Z" }, `readiness check "process" has an invalid, stale, or future observation time`},
		{"check at another version", func(o *readinessCheckOutput) { o.CheckResults[1].CheckVersion = "3" }, `required readiness check "network" did not pass`},
		{"missing result", func(o *readinessCheckOutput) { o.CheckResults = o.CheckResults[:1] }, "omitted one or more required checks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := passing()
			test.mutate(&output)
			if _, _, _, err := checkReadinessResults(input, output, time.Minute, now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("checkReadinessResults error = %v, want %q", err, test.want)
			}
		})
	}
}
