package backuppolicy

import (
	"errors"
	"math"
	"time"
)

// NextOccurrence returns the first scheduled time strictly after after. The
// anchor fixes the original wall-clock date for calendar intervals, so a
// monthly schedule anchored on January 31 returns February's last day and then
// returns March 31 instead of drifting to the 28th.
func NextOccurrence(anchor, after time.Time, interval Interval) (time.Time, error) {
	if anchor.IsZero() || after.IsZero() {
		return time.Time{}, errors.New("backup schedule anchor and comparison time are required")
	}
	if err := ValidateInterval(interval); err != nil {
		return time.Time{}, err
	}
	if after.Before(anchor) {
		return anchor, nil
	}

	switch interval.Unit {
	case Minute, Hour:
		unit := time.Minute
		if interval.Unit == Hour {
			unit = time.Hour
		}
		step := time.Duration(interval.Every) * unit
		elapsed := after.Sub(anchor)
		count := int64(elapsed/step) + 1
		if count > math.MaxInt64/int64(step) {
			return time.Time{}, errors.New("backup schedule has too many missed intervals")
		}
		return anchor.Add(time.Duration(count * int64(step))), nil
	case Day, Week:
		days := int64(interval.Every)
		if interval.Unit == Week {
			days *= 7
		}
		elapsedDays := civilDate(after).Sub(civilDate(anchor)).Hours() / 24
		index := int64(elapsedDays) / days
		candidate := anchor.AddDate(0, 0, int(index*days))
		if !candidate.After(after) {
			candidate = anchor.AddDate(0, 0, int((index+1)*days))
		}
		return candidate, nil
	case Month:
		elapsedMonths := int64(after.Year()-anchor.Year())*12 + int64(after.Month()-anchor.Month())
		index := elapsedMonths / int64(interval.Every)
		candidate := addCalendarMonths(anchor, index*int64(interval.Every))
		if !candidate.After(after) {
			candidate = addCalendarMonths(anchor, (index+1)*int64(interval.Every))
		}
		return candidate, nil
	case Year:
		index := int64(after.Year()-anchor.Year()) / int64(interval.Every)
		candidate := addCalendarMonths(anchor, index*int64(interval.Every)*12)
		if !candidate.After(after) {
			candidate = addCalendarMonths(anchor, (index+1)*int64(interval.Every)*12)
		}
		return candidate, nil
	default:
		return time.Time{}, errors.New("unsupported backup schedule unit")
	}
}

func civilDate(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func addCalendarMonths(anchor time.Time, months int64) time.Time {
	monthIndex := int64(anchor.Year())*12 + int64(anchor.Month()-1) + months
	year := int(monthIndex / 12)
	month := time.Month(monthIndex%12 + 1)
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day := anchor.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), anchor.Location())
}
