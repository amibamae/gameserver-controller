package controllerruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/workflow"
)

// runtimeBuild carries the inputs of one Build and the values its steps hand
// to later steps.
type runtimeBuild struct {
	ctx         context.Context
	value       config.Config
	configPath  string
	options     Options
	runtime     *Runtime
	definitions *workflow.Definitions
}

// runtimeBuildSteps are Build's steps in the order it runs them. Each step
// sets what it built on the runtime before reporting an error, so Close can
// release it.
var runtimeBuildSteps = []func(*runtimeBuild) error{
	(*runtimeBuild).loadSchemas,
	(*runtimeBuild).openStore,
	(*runtimeBuild).newRegistry,
	(*runtimeBuild).loadMonitorPolicy,
	(*runtimeBuild).registerAdapters,
	(*runtimeBuild).registerDefinitions,
	(*runtimeBuild).newEngine,
	(*runtimeBuild).recoverOperations,
	(*runtimeBuild).loadKeyring,
}

// newRuntimeBuild starts a build with an empty runtime that knows the
// configured server profiles.
func newRuntimeBuild(ctx context.Context, value config.Config, configPath string, options Options) *runtimeBuild {
	runtime := &Runtime{servers: map[string]config.ServerProfile{}, adapterEnvironments: map[string]string{}}
	for _, server := range value.Servers {
		runtime.servers[server.ID] = server
	}
	return &runtimeBuild{ctx: ctx, value: value, configPath: configPath, options: options, runtime: runtime}
}

// run runs every step in order and stops at the first failure.
func (b *runtimeBuild) run() error {
	for _, step := range runtimeBuildSteps {
		if err := step(b); err != nil {
			return err
		}
	}
	return nil
}

func (b *runtimeBuild) loadSchemas() (err error) {
	if b.runtime.Schemas, err = schemaregistry.Load(); err != nil {
		return fmt.Errorf("load protocol schemas: %w", err)
	}
	return nil
}

func (b *runtimeBuild) openStore() (err error) {
	path := config.ResolveStateStorePath(b.configPath, b.value.Controller.StateStore.Path)
	b.runtime.Store, err = store.OpenBolt(path)
	return err
}

func (b *runtimeBuild) newRegistry() (err error) {
	b.runtime.Registry, err = adapter.NewRegistry(b.runtime.Schemas)
	return err
}

func (b *runtimeBuild) loadMonitorPolicy() (err error) {
	if b.runtime.monitorPolicy, err = b.value.Controller.Monitoring.ProxyPolicy(); err != nil {
		return fmt.Errorf("controller.monitoring.proxy: %w", err)
	}
	return nil
}

func (b *runtimeBuild) registerAdapters() error {
	return b.runtime.registerAdapters(b.ctx, b.value, b.options)
}

// registerDefinitions registers the workflows in RegisteredOperationTypes.
func (b *runtimeBuild) registerDefinitions() error {
	b.definitions = workflow.NewDefinitions()
	define, err := workflow.NewServerDefineDefinition(b.runtime.Store)
	if err != nil {
		return err
	}
	return b.definitions.Register("server.define", define)
}

func (b *runtimeBuild) newEngine() (err error) {
	defaults := b.value.Controller.Defaults
	timeout, err := time.ParseDuration(defaults.OperationTimeout)
	if err != nil {
		return errors.New("controller.defaults.operation_timeout is invalid")
	}
	runtime := b.runtime
	runtime.Engine, err = workflow.NewEngine(runtime.Store, runtime.Registry, runtime.Schemas, b.definitions, denyResume{}, timeout, operationAttempts(defaults))
	return err
}

// operationAttempts is the first attempt plus the configured retries.
func operationAttempts(defaults config.Defaults) int {
	attempts := 1
	if defaults.RetryLimit != nil {
		attempts += *defaults.RetryLimit
	}
	return attempts
}

// recoverOperations marks steps a previous process left running as
// outcome_unknown and records the unfinished operations for the runner.
func (b *runtimeBuild) recoverOperations() error {
	recovered, err := b.runtime.Engine.Recover(b.ctx)
	if err != nil {
		return fmt.Errorf("recover durable operations: %w", err)
	}
	for _, operation := range recovered {
		b.runtime.recovered = append(b.runtime.recovered, operation.Record.OperationID)
	}
	return nil
}

func (b *runtimeBuild) loadKeyring() (err error) {
	b.runtime.keyring, err = buildKeyring(b.value.Controller.IdempotencyKeys, b.options.CredentialDirectory)
	return err
}
