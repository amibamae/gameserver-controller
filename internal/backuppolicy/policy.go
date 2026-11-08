// Package backuppolicy validates recurring backup schedules and retention.
// It selects policy only; it does not run backups or delete stored objects.
package backuppolicy

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

type Unit string

const (
	Minute Unit = "minutes"
	Hour   Unit = "hours"
	Day    Unit = "days"
	Week   Unit = "weeks"
	Month  Unit = "months"
	Year   Unit = "years"
)

// Interval uses calendar units for days, weeks, months, and years. Minute and
// hour intervals are elapsed time. This keeps month/year schedules meaningful
// without pretending those units have a fixed number of seconds.
type Interval struct {
	Every uint32 `yaml:"every" json:"every"`
	Unit  Unit   `yaml:"unit" json:"unit"`
}

type Retention struct {
	// KeepLast defaults to five. Zero means retain backups indefinitely.
	KeepLast *uint64 `yaml:"keep_last" json:"keep_last"`
}

type Policy struct {
	Schedule  *Interval  `yaml:"schedule" json:"schedule"`
	Retention *Retention `yaml:"retention" json:"retention"`
}

type Effective struct {
	Schedule Interval
	KeepLast uint64
}

func Default() Effective {
	return Effective{Schedule: Interval{Every: 9, Unit: Hour}, KeepLast: 5}
}

// Resolve applies defaults while preserving an explicit zero retention value.
func Resolve(policy Policy) (Effective, error) {
	effective := Default()
	if policy.Schedule != nil {
		effective.Schedule = *policy.Schedule
	}
	if policy.Retention != nil && policy.Retention.KeepLast != nil {
		effective.KeepLast = *policy.Retention.KeepLast
	}
	if err := ValidateInterval(effective.Schedule); err != nil {
		return Effective{}, err
	}
	return effective, nil
}

func ValidateInterval(interval Interval) error {
	if interval.Every == 0 {
		return errors.New("backup schedule every must be greater than zero")
	}
	maximum := map[Unit]uint32{
		Minute: 52_596_000, Hour: 876_600, Day: 36_525,
		Week: 5_218, Month: 1_200, Year: 100,
	}
	limit, ok := maximum[interval.Unit]
	if !ok {
		return fmt.Errorf("unsupported backup schedule unit %q", interval.Unit)
	}
	if interval.Every > limit {
		return fmt.Errorf("backup schedule exceeds the 100-year limit for %s", interval.Unit)
	}
	return nil
}

// Backup is a controller-owned inventory summary used to select retention
// candidates. Unverified and protected backups are never returned.
type Backup struct {
	ID        string
	CreatedAt time.Time
	Verified  bool
	Protected bool
}

// PruneCandidates returns the oldest verified, unprotected backups that exceed
// KeepLast. Callers must run it only after the new backup is verified, then
// delete each exact backup through the normal authorized storage workflow.
// KeepLast zero means unlimited retention.
func PruneCandidates(backups []Backup, keepLast uint64) ([]Backup, error) {
	if keepLast == 0 {
		return nil, nil
	}
	verified := make([]Backup, 0, len(backups))
	seen := make(map[string]struct{}, len(backups))
	for _, backup := range backups {
		if backup.ID == "" || backup.CreatedAt.IsZero() {
			return nil, errors.New("backup inventory requires an ID and creation time")
		}
		if _, exists := seen[backup.ID]; exists {
			return nil, fmt.Errorf("backup inventory repeats ID %q", backup.ID)
		}
		seen[backup.ID] = struct{}{}
		if backup.Verified {
			verified = append(verified, backup)
		}
	}
	if uint64(len(verified)) <= keepLast {
		return nil, nil
	}
	sort.Slice(verified, func(i, j int) bool {
		if verified[i].CreatedAt.Equal(verified[j].CreatedAt) {
			return verified[i].ID < verified[j].ID
		}
		return verified[i].CreatedAt.Before(verified[j].CreatedAt)
	})
	remaining := uint64(len(verified)) - keepLast
	candidates := make([]Backup, 0, remaining)
	for _, backup := range verified {
		if backup.Protected {
			continue
		}
		candidates = append(candidates, backup)
		remaining--
		if remaining == 0 {
			break
		}
	}
	return candidates, nil
}
