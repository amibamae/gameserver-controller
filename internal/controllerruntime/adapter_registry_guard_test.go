package controllerruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
)

// reviewedProductionAdapters is the reviewed set of implementation names the
// production controller runtime registers. Change it only in a reviewed pull
// request that adds or removes a real adapter_<name>.go implementation.
var reviewedProductionAdapters = []string{
	"cloudflare",
	"ntfy",
	"process",
	"smtp",
	"velocity",
	"wasabi",
	"webhook",
}

// adapterAllowlistAdvice tells a contributor how to add an adapter properly.
const adapterAllowlistAdvice = "Register a production adapter only for a real implementation " +
	"in its own adapter_<name>.go: no fakes, scripted replies or nil placeholders " +
	"(test doubles belong in a labelled test harness, used from _test.go files). " +
	"Then add its name to reviewedProductionAdapters in adapter_registry_guard_test.go " +
	"and have the change reviewed."

// registeredAdapterNames returns the implementation names registered by init.
func registeredAdapterNames() []string {
	names := make([]string, 0, len(adapterBuilders))
	for name := range adapterBuilders {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// absentFrom returns the names in names that are not in other, in order.
func absentFrom(names, other []string) []string {
	var absent []string
	for _, name := range names {
		if !slices.Contains(other, name) {
			absent = append(absent, name)
		}
	}
	return absent
}

// allowlistProblem describes how registered differs from reviewed, or returns
// "" when they hold the same names.
func allowlistProblem(registered, reviewed []string) string {
	unreviewed, missing := absentFrom(registered, reviewed), absentFrom(reviewed, registered)
	if len(unreviewed) == 0 && len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("production adapter registry differs from the reviewed allowlist: unreviewed %q, missing %q. %s",
		unreviewed, missing, adapterAllowlistAdvice)
}

func TestRegisteredAdaptersMatchReviewedAllowlist(t *testing.T) {
	if problem := allowlistProblem(registeredAdapterNames(), reviewedProductionAdapters); problem != "" {
		t.Fatal(problem)
	}
}

func TestAllowlistProblemNamesUnreviewedAndMissingAdapters(t *testing.T) {
	cases := []struct {
		name                 string
		registered, reviewed []string
		want                 string
	}{
		{"same", []string{"ntfy", "smtp"}, []string{"ntfy", "smtp"}, ""},
		{"unreviewed", []string{"ntfy", "vultr-hosting"}, []string{"ntfy"},
			`production adapter registry differs from the reviewed allowlist: unreviewed ["vultr-hosting"], missing []. ` + adapterAllowlistAdvice},
		{"missing", []string{"ntfy"}, []string{"ntfy", "smtp"},
			`production adapter registry differs from the reviewed allowlist: unreviewed [], missing ["smtp"]. ` + adapterAllowlistAdvice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allowlistProblem(tc.registered, tc.reviewed); got != tc.want {
				t.Fatalf("allowlistProblem =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// nilFuncImplementation is a non-pointer kind that can still hold nil.
type nilFuncImplementation func()

func (nilFuncImplementation) Descriptor() adapter.Descriptor { return adapter.Descriptor{} }

func (nilFuncImplementation) Invoke(context.Context, adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return adapter.InvokeResponse{}, nil
}

// valueImplementation is a struct value, which is never nil.
type valueImplementation struct{ adapter.Implementation }

func TestMissingImplementationDetectsNilAndTypedNil(t *testing.T) {
	cases := []struct {
		name           string
		implementation adapter.Implementation
		want           bool
	}{
		{"nil interface", nil, true},
		{"nil pointer", (*fakeadapter.Scripted)(nil), true},
		{"nil func", nilFuncImplementation(nil), true},
		{"pointer", fakeadapter.New(adapter.Descriptor{}), false},
		{"func", nilFuncImplementation(func() {}), false},
		{"struct value", valueImplementation{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingImplementation(tc.implementation); got != tc.want {
				t.Fatalf("missingImplementation = %v, want %v", got, tc.want)
			}
		})
	}
}

// builderReturning is a builder whose build returns built and err.
func builderReturning(built builtAdapter, err error) adapterBuilder {
	return adapterBuilder{
		check: func(adapterBuild) error { return nil },
		build: func(adapterBuild) (builtAdapter, error) { return built, err },
	}
}

func TestRegisterBuiltRefusesNilImplementation(t *testing.T) {
	monitor := &integrationmonitor.Target{}
	cases := []struct {
		name  string
		built builtAdapter
		err   error
		want  string
	}{
		{"nil", builtAdapter{monitor: monitor},
			nil, "adapter game.paper: implementation paper built no adapter; its builder must return a real implementation, not nil"},
		{"typed nil", builtAdapter{implementation: (*fakeadapter.Scripted)(nil), monitor: monitor},
			nil, "adapter game.paper: implementation paper built no adapter; its builder must return a real implementation, not nil"},
		{"build error wins", builtAdapter{}, errors.New("settings are missing"), "adapter game.paper: settings are missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := runtimeR6PartialRuntime(t)
			build := adapterBuild{profile: config.AdapterProfile{ID: "game.paper", Category: "game", Implementation: "paper"}}
			err := runtime.registerBuilt(builderReturning(tc.built, tc.err), build)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("registerBuilt error = %v, want %q", err, tc.want)
			}
			if len(runtime.AdapterIDs()) != 0 || len(runtime.monitorTargets) != 0 {
				t.Fatalf("adapters = %v, monitors = %d", runtime.AdapterIDs(), len(runtime.monitorTargets))
			}
		})
	}
}
