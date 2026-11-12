package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

func TestReadinessRefactorPreservesPreconditionFailures(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	operation, server, step := readinessFixture(schemas.ProtocolVersion())
	cases := []struct {
		name string
		stepID string
		now time.Time
		ttl time.Duration
		want string
	}{
		{"missing evidence step", "missing-step", readinessFixtureNow(), time.Minute, "not part of the persisted operation"},
		{"missing clock", step.StepID, time.Time{}, time.Minute, "clock is required"},
		{"invalid TTL", step.StepID, readinessFixtureNow(), 0, "TTL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildReadinessAssertionAt(schemas, operation, server, tc.stepID, tc.ttl, tc.now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}
