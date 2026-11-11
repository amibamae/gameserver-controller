// Package scalingpolicy decides when the controller may add or remove a
// backend server so that each server is used for the billing time already
// paid for.
//
// Hosting providers such as Vultr bill each instance in whole increments
// (one hour) from creation until it is destroyed. Destroying a server early
// in an hour saves nothing, and creating a new one later starts a new
// charge. So while a server is running, scaling decisions happen only in a
// short window near the end of each paid increment, by default minutes 58
// to 59 of every hour the server has run. Servers so cheap that holding
// them does not matter can opt out and scale at any time.
package scalingpolicy

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Settings is the configuration form, under a server's
// lifecycle.scaling block. Every field is optional.
type Settings struct {
	// BillingIncrement is the provider's billing unit. Default 1h.
	BillingIncrement string `yaml:"billing_increment" json:"billing_increment"`
	// DecisionWindowStart and DecisionWindowEnd bound the window, measured
	// from the start of each billing increment, in which a running server
	// may be scaled. Defaults 58m and 59m.
	DecisionWindowStart string `yaml:"decision_window_start" json:"decision_window_start"`
	DecisionWindowEnd   string `yaml:"decision_window_end" json:"decision_window_end"`
	// HourlyCost is the server's price per hour in the provider account's
	// currency, such as "0.036". Optional.
	HourlyCost string `yaml:"hourly_cost" json:"hourly_cost"`
	// IgnoreBillingBelowHourlyCost turns the window off for servers whose
	// HourlyCost is below this amount. Requires HourlyCost.
	IgnoreBillingBelowHourlyCost string `yaml:"ignore_billing_below_hourly_cost" json:"ignore_billing_below_hourly_cost"`
}

// Policy is the validated form of Settings.
type Policy struct {
	BillingIncrement time.Duration
	WindowStart      time.Duration
	WindowEnd        time.Duration
	// BillingAware is false when the server is cheap enough that it may be
	// scaled at any time.
	BillingAware bool
}

// Defaults: one-hour billing, decisions in minutes 58 to 59.
const (
	DefaultBillingIncrement = time.Hour
	DefaultWindowStart      = 58 * time.Minute
	DefaultWindowEnd        = 59 * time.Minute
)

var costPattern = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,6})?$`)

// Resolve validates settings and applies defaults.
func Resolve(settings Settings) (Policy, error) {
	policy := Policy{BillingIncrement: DefaultBillingIncrement, WindowStart: DefaultWindowStart, WindowEnd: DefaultWindowEnd, BillingAware: true}
	parse := func(value string, target *time.Duration, name string) error {
		if value == "" {
			return nil
		}
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 {
			return fmt.Errorf("scaling %s must be a non-negative duration", name)
		}
		*target = parsed
		return nil
	}
	if err := parse(settings.BillingIncrement, &policy.BillingIncrement, "billing_increment"); err != nil {
		return Policy{}, err
	}
	if err := parse(settings.DecisionWindowStart, &policy.WindowStart, "decision_window_start"); err != nil {
		return Policy{}, err
	}
	if err := parse(settings.DecisionWindowEnd, &policy.WindowEnd, "decision_window_end"); err != nil {
		return Policy{}, err
	}
	if policy.BillingIncrement < time.Minute || policy.BillingIncrement > 31*24*time.Hour {
		return Policy{}, errors.New("scaling billing_increment must be between 1m and 744h")
	}
	if policy.WindowEnd <= policy.WindowStart || policy.WindowEnd > policy.BillingIncrement {
		return Policy{}, errors.New("scaling decision window must end after it starts and within the billing increment")
	}
	if settings.IgnoreBillingBelowHourlyCost != "" {
		if settings.HourlyCost == "" {
			return Policy{}, errors.New("scaling ignore_billing_below_hourly_cost requires hourly_cost")
		}
		cost, err := parseCost(settings.HourlyCost)
		if err != nil {
			return Policy{}, fmt.Errorf("scaling hourly_cost: %w", err)
		}
		threshold, err := parseCost(settings.IgnoreBillingBelowHourlyCost)
		if err != nil {
			return Policy{}, fmt.Errorf("scaling ignore_billing_below_hourly_cost: %w", err)
		}
		policy.BillingAware = cost >= threshold
	} else if settings.HourlyCost != "" {
		if _, err := parseCost(settings.HourlyCost); err != nil {
			return Policy{}, fmt.Errorf("scaling hourly_cost: %w", err)
		}
	}
	return policy, nil
}

// parseCost reads a decimal amount as millionths, avoiding floating point.
func parseCost(value string) (int64, error) {
	if !costPattern.MatchString(value) {
		return 0, errors.New("must be a decimal amount such as 0.036")
	}
	whole, fraction, _ := strings.Cut(value, ".")
	fraction = (fraction + "000000")[:6]
	w, _ := strconv.ParseInt(whole, 10, 64)
	f, _ := strconv.ParseInt(fraction, 10, 64)
	if w > math.MaxInt64/1_000_000 {
		return 0, errors.New("is too large")
	}
	return w*1_000_000 + f, nil
}

// Window returns the decision window that is open at now, or the next one,
// for a server created at createdAt.
func (p Policy) Window(createdAt, now time.Time) (start, end time.Time) {
	elapsed := now.Sub(createdAt)
	if elapsed < 0 {
		elapsed = 0
	}
	increment := elapsed / p.BillingIncrement
	start = createdAt.Add(time.Duration(increment)*p.BillingIncrement + p.WindowStart)
	end = createdAt.Add(time.Duration(increment)*p.BillingIncrement + p.WindowEnd)
	if !now.Before(end) {
		start = start.Add(p.BillingIncrement)
		end = end.Add(p.BillingIncrement)
	}
	return start, end
}

// InWindow reports whether a running server may be scaled now.
func (p Policy) InWindow(createdAt, now time.Time) bool {
	if !p.BillingAware {
		return true
	}
	start, end := p.Window(createdAt, now)
	return !now.Before(start) && now.Before(end)
}

// Decision says whether a scaling action may start now, and if not, when to
// check again.
type Decision struct {
	Allowed bool
	// NotBefore is the earliest time the action may start when not allowed.
	NotBefore time.Time
	Reason    string
}

// ScaleDown decides whether an idle server may be shut down and destroyed.
// idleSince is when the server last had no players; the zero time means it
// has players. idleShutdown is the minimum idle time (lifecycle.idle_shutdown).
func (p Policy) ScaleDown(createdAt, now, idleSince time.Time, idleShutdown time.Duration) Decision {
	if idleSince.IsZero() {
		return Decision{Reason: "server_has_players"}
	}
	idleUntil := idleSince.Add(idleShutdown)
	if now.Before(idleUntil) {
		next := idleUntil
		if p.BillingAware {
			next, _ = p.Window(createdAt, idleUntil)
			if !idleUntil.Before(next) {
				next = idleUntil
			}
		}
		return Decision{NotBefore: next, Reason: "not_idle_long_enough"}
	}
	if !p.BillingAware {
		return Decision{Allowed: true, Reason: "billing_window_not_required"}
	}
	if p.InWindow(createdAt, now) {
		return Decision{Allowed: true, Reason: "in_decision_window"}
	}
	start, _ := p.Window(createdAt, now)
	return Decision{NotBefore: start, Reason: "waiting_for_decision_window"}
}

// ScaleUp decides whether another server may be added. With no server
// running, players are waiting, so it is always allowed. With a server
// running, adding capacity waits for that server's decision window, so the
// already-paid hour is used first.
func (p Policy) ScaleUp(running bool, runningCreatedAt, now time.Time) Decision {
	if !running {
		return Decision{Allowed: true, Reason: "no_server_running"}
	}
	if p.InWindow(runningCreatedAt, now) {
		return Decision{Allowed: true, Reason: "in_decision_window"}
	}
	start, _ := p.Window(runningCreatedAt, now)
	return Decision{NotBefore: start, Reason: "waiting_for_decision_window"}
}
