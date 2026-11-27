package velocityproxy

import (
	"context"
	"time"
)

// boundState maps a proxy observation to the protocol state of the exact
// requested instance. A backend name bound to another instance, or to an
// address that no longer matches the instance's current address, never
// reports the requested instance as routable.
func (a *Adapter) boundState(ctx context.Context, target BackendTarget, observed BackendObservation) (RegistrationState, string) {
	switch observed.Registration {
	case Unregistered:
		return Unregistered, "not_routable"
	case Registered, Draining:
		if observed.InstanceRef == nil || *observed.InstanceRef != target.InstanceRef {
			return Unknown, "not_routable"
		}
		if observed.Registration == Draining {
			return Draining, "not_routable"
		}
		if !observed.Routable {
			return Registered, "not_routable"
		}
		current, err := a.addresses.ResolveBackendAddress(ctx, target)
		if err != nil {
			return Unknown, "unknown"
		}
		if !sameAddress(observed.Address, current) {
			// The instance's address changed since registration.
			return Unknown, "not_routable"
		}
		return Registered, "routable"
	default:
		return Unknown, "unknown"
	}
}

// backendView is the protocol state of the exact requested instance, bound
// from one proxy observation.
type backendView struct {
	State       RegistrationState
	Routability string
	ObservedAt  time.Time
}

// viewBackend binds an observation to the target with boundState and stamps
// it with the observation time, or the adapter's clock when that is missing.
func (a *Adapter) viewBackend(ctx context.Context, target BackendTarget, observed BackendObservation) backendView {
	state, routability := a.boundState(ctx, target, observed)
	return backendView{State: state, Routability: routability, ObservedAt: a.observedAt(observed.ObservedAt)}
}

// boundToInstance reports whether the backend is registered or draining for
// the exact requested instance.
func (view backendView) boundToInstance() bool {
	return view.State == Registered || view.State == Draining
}

// confirms reports whether the view shows the wanted state; a registration
// is confirmed only once it is also routable.
func (view backendView) confirms(want RegistrationState) bool {
	return view.State == want && (want != Registered || view.Routability == "routable")
}

// knownSessionCount copies the observed active session count, or returns nil
// when the proxy reported none or a negative count.
func knownSessionCount(observed BackendObservation) *int64 {
	if observed.ActiveSessions == nil || *observed.ActiveSessions < 0 {
		return nil
	}
	count := *observed.ActiveSessions
	return &count
}

func (a *Adapter) observedAt(observed time.Time) time.Time {
	if observed.IsZero() {
		return a.now().UTC()
	}
	return observed.UTC()
}

// fresh reports whether an observation is not in the future and no older
// than maxAgeSeconds.
func (a *Adapter) fresh(observed time.Time, maxAgeSeconds int) bool {
	now := a.now()
	return !observed.IsZero() && !observed.After(now) && now.Sub(observed) <= time.Duration(maxAgeSeconds)*time.Second
}
