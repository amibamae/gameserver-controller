package backuppolicy

import (
	"reflect"
	"testing"
	"time"
)

func TestResolveDefaultsAndExplicitUnlimitedRetention(t *testing.T) {
	defaults, err := Resolve(Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Schedule != (Interval{Every: 9, Unit: Hour}) || defaults.KeepLast != 5 {
		t.Fatalf("defaults = %#v, want every 9 hours and five backups", defaults)
	}

	zero := uint64(0)
	custom, err := Resolve(Policy{Retention: &Retention{KeepLast: &zero}})
	if err != nil {
		t.Fatal(err)
	}
	if custom.KeepLast != 0 {
		t.Fatalf("explicit zero retention = %d, want unlimited", custom.KeepLast)
	}
}

func TestResolveSupportsMinuteThroughYearSchedules(t *testing.T) {
	for _, interval := range []Interval{
		{Every: 5, Unit: Minute}, {Every: 9, Unit: Hour}, {Every: 2, Unit: Day},
		{Every: 2, Unit: Week}, {Every: 1, Unit: Month}, {Every: 1, Unit: Year},
	} {
		if _, err := Resolve(Policy{Schedule: &interval}); err != nil {
			t.Errorf("Resolve(%#v): %v", interval, err)
		}
	}
	for _, interval := range []Interval{{Unit: Hour}, {Every: 1, Unit: "fortnights"}, {Every: 1_201, Unit: Month}} {
		if err := ValidateInterval(interval); err == nil {
			t.Errorf("ValidateInterval(%#v) succeeded", interval)
		}
	}
}

func TestPruneCandidatesKeepsNewestVerifiedBackupsAndProtectedHistory(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	backups := []Backup{
		{ID: "b1", CreatedAt: base, Verified: true},
		{ID: "b2", CreatedAt: base.Add(time.Hour), Verified: true, Protected: true},
		{ID: "b3", CreatedAt: base.Add(2 * time.Hour), Verified: true},
		{ID: "b4", CreatedAt: base.Add(3 * time.Hour), Verified: true},
		{ID: "b5", CreatedAt: base.Add(4 * time.Hour), Verified: true},
		{ID: "b6", CreatedAt: base.Add(5 * time.Hour), Verified: true},
		{ID: "uploading", CreatedAt: base.Add(6 * time.Hour), Verified: false},
	}
	candidates, err := PruneCandidates(backups, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(candidates), []string{"b1"}) {
		t.Fatalf("prune candidates = %v, want oldest verified unprotected backup b1", ids(candidates))
	}
	if all, err := PruneCandidates(backups, 0); err != nil || len(all) != 0 {
		t.Fatalf("unlimited retention candidates = %v, err=%v", ids(all), err)
	}
}

func TestPruneCandidatesRejectsAmbiguousInventory(t *testing.T) {
	created := time.Now().UTC()
	for name, inventory := range map[string][]Backup{
		"missing ID":   {{CreatedAt: created, Verified: true}},
		"missing time": {{ID: "backup", Verified: true}},
		"duplicate ID": {{ID: "same", CreatedAt: created}, {ID: "same", CreatedAt: created.Add(time.Minute)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PruneCandidates(inventory, 1); err == nil {
				t.Fatal("ambiguous inventory was accepted")
			}
		})
	}
}

func ids(backups []Backup) []string {
	result := make([]string, len(backups))
	for i, backup := range backups {
		result[i] = backup.ID
	}
	return result
}
