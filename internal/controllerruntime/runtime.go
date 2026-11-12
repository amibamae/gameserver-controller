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
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/adapterprocess"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/credential"
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
	for _, profile := range value.Adapters {
		if profile.Implementation == "" {
			continue
		}
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

// Build composes the runtime. On any failure it closes what it already built.
// Startup recovery marks steps left running by a previous process as
// outcome_unknown; it makes no adapter calls. A recovery failure stops
// startup, because serving with unrecoverable durable state is unsafe.
func Build(ctx context.Context, value config.Config, configPath string, options Options) (_ *Runtime, err error) {
	if ctx == nil || options.ControllerVersion == "" {
		return nil, errors.New("context and controller version are required")
	}
	if err := Validate(value); err != nil {
		return nil, err
	}
	runtime := &Runtime{servers: map[string]config.ServerProfile{}, adapterEnvironments: map[string]string{}}
	for _, server := range value.Servers {
		runtime.servers[server.ID] = server
	}
	defer func() {
		if err != nil {
			runtime.Close()
		}
	}()
	if runtime.Schemas, err = schemaregistry.Load(); err != nil {
		return nil, fmt.Errorf("load protocol schemas: %w", err)
	}
	if runtime.Store, err = store.OpenBolt(config.ResolveStateStorePath(configPath, value.Controller.StateStore.Path)); err != nil {
		return nil, err
	}
	if runtime.Registry, err = adapter.NewRegistry(runtime.Schemas); err != nil {
		return nil, err
	}
	if err = runtime.registerAdapters(ctx, value, options); err != nil {
		return nil, err
	}
	definitions := workflow.NewDefinitions()
	define, err := workflow.NewServerDefineDefinition(runtime.Store)
	if err != nil {
		return nil, err
	}
	if err = definitions.Register("server.define", define); err != nil {
		return nil, err
	}
	timeout, err := time.ParseDuration(value.Controller.Defaults.OperationTimeout)
	if err != nil {
		return nil, errors.New("controller.defaults.operation_timeout is invalid")
	}
	attempts := 1
	if value.Controller.Defaults.RetryLimit != nil {
		attempts += *value.Controller.Defaults.RetryLimit
	}
	if runtime.Engine, err = workflow.NewEngine(runtime.Store, runtime.Registry, runtime.Schemas, definitions, denyResume{}, timeout, attempts); err != nil {
		return nil, err
	}
	if _, err = runtime.Engine.Recover(ctx); err != nil {
		return nil, fmt.Errorf("recover durable operations: %w", err)
	}
	return runtime, nil
}

func (r *Runtime) registerAdapters(ctx context.Context, value config.Config, options Options) error {
	var resolver *credential.Resolver
	for _, profile := range value.Adapters {
		if profile.Implementation == "" {
			continue
		}
		builder, ok := adapterBuilders[profile.Implementation]
		if !ok {
			return fmt.Errorf("adapter %s: unsupported implementation %s", profile.ID, profile.Implementation)
		}
		profileID := profile.ID
		built, err := builder.build(adapterBuild{
			ctx: ctx, value: value, profile: profile, protocolVersion: r.Schemas.ProtocolVersion(), options: options, schemas: r.Schemas,
			credentials: func() (credentialResolver, error) {
				if resolver == nil {
					var err error
					if resolver, err = credential.NewResolver(options.CredentialDirectory); err != nil {
						return nil, fmt.Errorf("adapter %s needs systemd credentials: %w", profileID, err)
					}
				}
				return resolver, nil
			},
		})
		if built.process != nil {
			r.processes = append(r.processes, built.process)
		}
		if err != nil {
			return fmt.Errorf("adapter %s: %w", profile.ID, err)
		}
		if built.unreachableProxy {
			r.unreachableProxies = append(r.unreachableProxies, profile.ID)
		}
		if environment := config.BoundEnvironment(profile); environment != "" {
			r.adapterEnvironments[profile.ID] = environment
		}
		if err := r.Registry.Register(built.implementation); err != nil {
			return fmt.Errorf("adapter %s: %w", profile.ID, err)
		}
		r.adapters = append(r.adapters, profile.ID)
	}
	return nil
}

// Close stops process adapters, then closes the store. It is safe to call on
// a partially built runtime.
func (r *Runtime) Close() error {
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
