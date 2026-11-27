// Package controllerruntime builds the controller's long-lived services from a
// validated configuration: schemas, the state store, the adapter registry with
// its configured adapters, workflow definitions, and the workflow engine.
package controllerruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/acceptance"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/adapterprocess"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/workflow"
)

// RegisteredOperationTypes lists the workflows this runtime can execute.
// Other lifecycle workflows need hosting, execution, and game adapters that
// do not exist yet; their operations are not run.
var RegisteredOperationTypes = []string{"server.define"}

// Options carries process-level inputs that are not part of the config file.
type Options struct {
	// CredentialDirectory is systemd's CREDENTIALS_DIRECTORY. It is required
	// only when a configured adapter needs credentials.
	CredentialDirectory string
	// ControllerVersion identifies this build to process adapters.
	ControllerVersion string
}

// Runtime owns the services built from configuration. Close releases them.
type Runtime struct {
	Store     *store.BoltStore
	Schemas   *schemaregistry.Registry
	Registry  *adapter.Registry
	Engine    *workflow.Engine
	processes []*adapterprocess.Process
	adapters  []string
	// unreachableProxies are Velocity adapters whose bridge did not answer at
	// startup. They are registered, but every proxy call is refused until the
	// bridge passes its identity check.
	unreachableProxies []string
	// servers are the configured server profiles, for network checks.
	servers map[string]config.ServerProfile
	// adapterEnvironments records the environment each environment-bound
	// adapter was built for. Adapters without one cannot satisfy a network
	// requirement.
	adapterEnvironments map[string]string
	// monitorTargets and destinations feed the outage monitor.
	monitorTargets []integrationmonitor.Target
	destinations   []integrationmonitor.Destination
	monitorPolicy  integrationmonitor.Policy
	stopMonitoring func()
	// keyring binds management API idempotency keys; nil without
	// controller.idempotency_keys.
	keyring *acceptance.Keyring
	// recovered are the unfinished operations found at startup, which the
	// runner resumes.
	recovered []string
	runner    *runner
}

// AdapterIDs returns the IDs of the adapters that were started and registered.
func (r *Runtime) AdapterIDs() []string { return append([]string(nil), r.adapters...) }

// UnreachableProxies returns the Velocity adapters whose bridge was not
// reachable at startup.
func (r *Runtime) UnreachableProxies() []string {
	return append([]string(nil), r.unreachableProxies...)
}

// Validate checks implemented adapter profiles without starting anything,
// opening the store, or reading credentials. Each implementation's own file
// supplies the check (see adapter_registry.go).
func Validate(value config.Config) error {
	var problems []string
	for _, profile := range implementedProfiles(value) {
		builder, ok := adapterBuilders[profile.Implementation]
		if !ok {
			problems = append(problems, fmt.Sprintf("adapter %s: implementation %s has no runtime builder", profile.ID, profile.Implementation))
			continue
		}
		build := adapterBuild{value: value, profile: profile, protocolVersion: "validation",
			credentials: func() (credentialResolver, error) { return unavailableCredentials{}, nil }}
		if err := builder.check(build); err != nil {
			problems = append(problems, fmt.Sprintf("adapter %s: %v", profile.ID, err))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("adapter configuration is invalid:\n - %s", strings.Join(problems, "\n - "))
	}
	return nil
}

// Build composes the runtime: schemas, store, adapter registry, monitoring
// policy, adapters (creating the credential resolver when one first needs
// it), workflow definitions, engine, recovery, and the idempotency keyring,
// in that order (see runtimeBuildSteps). On any failure it closes what it
// already built. Startup recovery marks steps left running by a previous
// process as outcome_unknown; it makes no adapter calls. A recovery failure
// stops startup, because serving with unrecoverable durable state is unsafe.
func Build(ctx context.Context, value config.Config, configPath string, options Options) (*Runtime, error) {
	if ctx == nil || options.ControllerVersion == "" {
		return nil, errors.New("context and controller version are required")
	}
	if err := Validate(value); err != nil {
		return nil, err
	}
	build := newRuntimeBuild(ctx, value, configPath, options)
	if err := build.run(); err != nil {
		// The build error is the one to report. Close still stops every
		// process and closes the store before returning its own error, so
		// nothing is left running; that error would only hide the cause.
		_ = build.runtime.Close()
		return nil, err
	}
	return build.runtime, nil
}

// MonitoredIntegrations returns the adapter IDs the outage monitor probes.
func (r *Runtime) MonitoredIntegrations() []string {
	return adapterIDsOf(r.monitorTargets, func(target integrationmonitor.Target) string { return target.Subject.AdapterID })
}

// Notifiers returns the notifier adapter IDs that receive monitor events.
func (r *Runtime) Notifiers() []string {
	return adapterIDsOf(r.destinations, func(destination integrationmonitor.Destination) string { return destination.AdapterID })
}

// adapterIDsOf returns the adapter ID of each item, in order.
func adapterIDsOf[T any](items []T, adapterID func(T) string) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, adapterID(item))
	}
	return ids
}

// StartMonitoring starts the proxy outage monitor and the notification
// dispatcher. Every event also goes to log, the controller log channel. It
// does nothing when no proxy is monitored. Close stops it.
func (r *Runtime) StartMonitoring(ctx context.Context, log integrationmonitor.Logger) {
	if len(r.monitorTargets) == 0 || r.stopMonitoring != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	dispatcher := integrationmonitor.NewDispatcher(r.Registry, r.destinations, log)
	monitor := integrationmonitor.New(r.monitorPolicy, r.monitorTargets, dispatcher.Publish)
	dispatcher.Start(ctx)
	monitor.Start(ctx)
	r.stopMonitoring = func() {
		cancel()
		monitor.Wait()
		dispatcher.Wait()
	}
}

// Close stops monitoring and process adapters, then closes the store. It is
// safe to call on a partially built runtime.
func (r *Runtime) Close() error {
	if r.stopMonitoring != nil {
		r.stopMonitoring()
		r.stopMonitoring = nil
	}
	if r.runner != nil {
		r.runner.stop()
		r.runner = nil
	}
	var failures []error
	for i := len(r.processes) - 1; i >= 0; i-- {
		if err := r.processes[i].Close(); err != nil {
			failures = append(failures, err)
		}
	}
	r.processes = nil
	if r.Store != nil {
		if err := r.Store.Close(); err != nil {
			failures = append(failures, err)
		}
		r.Store = nil
	}
	return errors.Join(failures...)
}

// denyResume refuses every resume. Leaving needs_operator requires an
// authenticated operator approval route, which does not exist yet.
type denyResume struct{}

func (denyResume) AuthorizeResume(context.Context, string, store.OperationRecord) error {
	return errors.New("operator resume is not available in this controller build")
}
