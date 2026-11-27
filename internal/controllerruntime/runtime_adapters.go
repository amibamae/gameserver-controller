package controllerruntime

import (
	"context"
	"fmt"
	"reflect"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/credential"
)

// implementedProfiles returns the adapter profiles that name an
// implementation, in configuration order. Declared-only profiles are neither
// validated nor built.
func implementedProfiles(value config.Config) []config.AdapterProfile {
	var profiles []config.AdapterProfile
	for _, profile := range value.Adapters {
		if profile.Implementation != "" {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

// registerAdapters builds and registers every implemented adapter in
// configuration order and stops at the first failure.
func (r *Runtime) registerAdapters(ctx context.Context, value config.Config, options Options) error {
	credentials := &lazyCredentials{directory: options.CredentialDirectory}
	base := adapterBuild{ctx: ctx, value: value, options: options}
	for _, profile := range implementedProfiles(value) {
		if err := r.buildAndRegister(base, profile, credentials); err != nil {
			return err
		}
	}
	return nil
}

// buildAndRegister builds one adapter and registers it. A started process is
// kept even when a later step fails, so Close stops it.
func (r *Runtime) buildAndRegister(build adapterBuild, profile config.AdapterProfile, credentials *lazyCredentials) error {
	builder, ok := adapterBuilders[profile.Implementation]
	if !ok {
		return fmt.Errorf("adapter %s: unsupported implementation %s", profile.ID, profile.Implementation)
	}
	build.profile = profile
	build.protocolVersion = r.Schemas.ProtocolVersion()
	build.schemas = r.Schemas
	build.credentials = credentials.forAdapter(profile.ID)
	return r.registerBuilt(builder, build)
}

// registerBuilt runs builder for build.profile and registers what it built.
// A builder that returns no implementation is refused before anything is
// tracked: a production adapter must be a real implementation, never a nil
// placeholder.
func (r *Runtime) registerBuilt(builder adapterBuilder, build adapterBuild) error {
	profile := build.profile
	built, err := builder.build(build)
	if built.process != nil {
		r.processes = append(r.processes, built.process)
	}
	if err != nil {
		return fmt.Errorf("adapter %s: %w", profile.ID, err)
	}
	if missingImplementation(built.implementation) {
		return fmt.Errorf("adapter %s: implementation %s built no adapter; its builder must return a real implementation, not nil", profile.ID, profile.Implementation)
	}
	r.track(profile, built)
	if err := r.Registry.Register(built.implementation); err != nil {
		return fmt.Errorf("adapter %s: %w", profile.ID, err)
	}
	r.adapters = append(r.adapters, profile.ID)
	return nil
}

// missingImplementation reports a nil implementation, including a typed nil
// such as a nil pointer stored in the interface.
func missingImplementation(implementation adapter.Implementation) bool {
	if implementation == nil {
		return true
	}
	value := reflect.ValueOf(implementation)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	default:
		return false
	}
}

// track records what the runtime needs to know about a built adapter: an
// unreachable proxy, a monitor target, a notifier destination, and the
// environment it is bound to.
func (r *Runtime) track(profile config.AdapterProfile, built builtAdapter) {
	if built.unreachableProxy {
		r.unreachableProxies = append(r.unreachableProxies, profile.ID)
	}
	if built.monitor != nil {
		r.monitorTargets = append(r.monitorTargets, *built.monitor)
	}
	if built.destination != nil {
		r.destinations = append(r.destinations, *built.destination)
	}
	if environment := config.BoundEnvironment(profile); environment != "" {
		r.adapterEnvironments[profile.ID] = environment
	}
}

// lazyCredentials creates the systemd credential resolver on first use and
// shares it between adapters. A failed creation is retried by the next
// adapter that needs credentials.
type lazyCredentials struct {
	directory string
	resolver  *credential.Resolver
}

// forAdapter returns the credentials accessor for one adapter; its error
// names that adapter.
func (c *lazyCredentials) forAdapter(adapterID string) func() (credentialResolver, error) {
	return func() (credentialResolver, error) {
		if c.resolver != nil {
			return c.resolver, nil
		}
		resolver, err := credential.NewResolver(c.directory)
		if err != nil {
			return nil, fmt.Errorf("adapter %s needs systemd credentials: %w", adapterID, err)
		}
		c.resolver = resolver
		return resolver, nil
	}
}
