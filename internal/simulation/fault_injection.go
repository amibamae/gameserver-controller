// Package simulation provides isolated, deterministic test infrastructure for controller operations without real cloud resources.
package simulation

import (
	"errors"
	"sync/atomic"
)

var (
	ErrNetworkTimeout     = errors.New("simulated network timeout")
	ErrConnectionRefused  = errors.New("simulated connection refused")
	ErrStorageUnavailable = errors.New("simulated storage unavailable")
	ErrOperationCancelled = errors.New("simulated operation cancelled")
	ErrGenericFailure     = errors.New("simulated generic failure")
)

type FaultInjector interface {
	InjectFailure() error
}

type faultInjector struct{}

func NewFaultInjector() FaultInjector { return &faultInjector{} }

func (f *faultInjector) InjectFailure() error {
	return ErrNetworkTimeout
}
