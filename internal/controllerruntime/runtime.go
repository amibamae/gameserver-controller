// Package controllerruntime builds the controller's long-lived services from a
// validated configuration: schemas, the state store, the adapter registry with
// its configured adapters, workflow definitions, and the workflow engine.
package controllerruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/adapterprocess"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/wasabistorage"
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
}

// AdapterIDs returns the IDs of the adapters that were started and registered.
func (r *Runtime) AdapterIDs() []string { return append([]string(nil), r.adapters...) }

// UnreachableProxies returns the Velocity adapters whose bridge was not
// reachable at startup.
func (r *Runtime) UnreachableProxies() []string {
	return append([]string(nil), r.unreachableProxies...)
}

// Validate checks implemented adapter profiles without starting anything,
// opening the store, or reading credentials.
func Validate(value config.Config) error {
	var problems []string
	for _, profile := range value.Adapters {
		switch profile.Implementation {
		case config.ImplementationWasabi:
			if err := wasabistorage.ValidateConfig(wasabiConfig(profile)); err != nil {
				problems = append(problems, fmt.Sprintf("adapter %s: %v", profile.ID, err))
			}
		case config.ImplementationProcess:
			info, err := os.Stat(profile.Process.Path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				problems = append(problems, fmt.Sprintf("adapter %s: process.path is not an executable file", profile.ID))
			}
		case config.ImplementationVelocity:
			if _, err := buildVelocity(value, profile, "validation", unavailableCredentials{}); err != nil {
				problems = append(problems, fmt.Sprintf("adapter %s: %v", profile.ID, err))
			}
		case config.ImplementationCloudflare:
			if _, err := buildCloudflare(value, profile, "validation", unavailableCredentials{}); err != nil {
				problems = append(problems, fmt.Sprintf("adapter %s: %v", profile.ID, err))
			}
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
	runtime := &Runtime{}
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
	credentials := func(profileID string) (*credential.Resolver, error) {
		if resolver == nil {
			var err error
			if resolver, err = credential.NewResolver(options.CredentialDirectory); err != nil {
				return nil, fmt.Errorf("adapter %s needs systemd credentials: %w", profileID, err)
			}
		}
		return resolver, nil
	}
	for _, profile := range value.Adapters {
		var implementation adapter.Implementation
		switch profile.Implementation {
		case "":
			continue
		case config.ImplementationWasabi:
			resolver, err := credentials(profile.ID)
			if err != nil {
				return err
			}
			wasabi, err := wasabistorage.New(wasabiConfig(profile), r.Schemas.ProtocolVersion(), resolver)
			if err != nil {
				return fmt.Errorf("adapter %s: %w", profile.ID, err)
			}
			implementation = wasabi
		case config.ImplementationProcess:
			process, err := adapterprocess.Start(ctx, r.Schemas, processConfig(profile, options))
			if err != nil {
				return fmt.Errorf("adapter %s: %w", profile.ID, err)
			}
			r.processes = append(r.processes, process)
			// A configured executable is not automatically trusted.
			descriptor := process.Descriptor()
			if descriptor.AdapterID != profile.ID || string(descriptor.Category) != profile.Category {
				return fmt.Errorf("adapter %s: process reported identity %q (%s), not the configured one", profile.ID, descriptor.AdapterID, descriptor.Category)
			}
			implementation = process
		case config.ImplementationVelocity:
			resolver, err := credentials(profile.ID)
			if err != nil {
				return err
			}
			parts, err := buildVelocity(value, profile, r.Schemas.ProtocolVersion(), resolver)
			if err != nil {
				return fmt.Errorf("adapter %s: %w", profile.ID, err)
			}
			reachable, err := parts.bridge.startupCheck(ctx)
			if err != nil {
				return fmt.Errorf("adapter %s: %w", profile.ID, err)
			}
			if !reachable {
				r.unreachableProxies = append(r.unreachableProxies, profile.ID)
			}
			implementation = parts.adapter
		case config.ImplementationCloudflare:
			resolver, err := credentials(profile.ID)
			if err != nil {
				return err
			}
			cloudflare, err := buildCloudflare(value, profile, r.Schemas.ProtocolVersion(), resolver)
			if err != nil {
				return fmt.Errorf("adapter %s: %w", profile.ID, err)
			}
			implementation = cloudflare
		default:
			return fmt.Errorf("adapter %s: unsupported implementation", profile.ID)
		}
		if err := r.Registry.Register(implementation); err != nil {
			return fmt.Errorf("adapter %s: %w", profile.ID, err)
		}
		r.adapters = append(r.adapters, profile.ID)
	}
	return nil
}

func wasabiConfig(profile config.AdapterProfile) wasabistorage.Config {
	w := profile.Wasabi
	if w == nil {
		w = &config.WasabiProfile{}
	}
	return wasabistorage.Config{
		AdapterID: profile.ID, StorageProfileID: w.StorageProfileID, EnvironmentID: w.EnvironmentID,
		Container: w.Container, KeyPrefix: w.KeyPrefix, Region: w.Region, EndpointURL: w.EndpointURL,
		AccessKeyIDRef:     profile.SecretRefs[config.WasabiAccessKeyIDSecret].SecretRef,
		SecretAccessKeyRef: profile.SecretRefs[config.WasabiSecretAccessKeySecret].SecretRef,
		MaxObjectBytes:     w.MaxObjectBytes, RequireVersionIDs: w.RequireVersionIDs,
	}
}

func processConfig(profile config.AdapterProfile, options Options) adapterprocess.Config {
	p := profile.Process
	start, _ := time.ParseDuration(p.StartTimeout)
	stop, _ := time.ParseDuration(p.StopTimeout)
	return adapterprocess.Config{
		Path: p.Path, Args: p.Args, Env: p.Env, Dir: p.WorkingDirectory,
		ControllerID: "gameserver-controller", ControllerVersion: options.ControllerVersion,
		StartTimeout: start, StopTimeout: stop,
	}
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
