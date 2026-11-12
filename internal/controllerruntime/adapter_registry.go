package controllerruntime

import (
	"context"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/adapterprocess"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// Each adapter implementation lives in its own adapter_<name>.go file. That
// file defines the implementation's settings block, validates it, builds the
// adapter, and registers both halves from init() with registerAdapter. To add
// an adapter, add such a file; to remove one, delete its file.

// adapterBuild is what a builder receives for one configured profile.
type adapterBuild struct {
	ctx             context.Context
	value           config.Config
	profile         config.AdapterProfile
	protocolVersion string
	options         Options
	schemas         *schemaregistry.Registry
	// credentials returns the systemd credential resolver, creating it on
	// first use. During Validate it returns one that refuses every read.
	credentials func() (credentialResolver, error)
}

// builtAdapter is a built adapter plus anything the runtime must track.
type builtAdapter struct {
	implementation adapter.Implementation
	// process is a started adapter process the runtime must stop on Close.
	// Builders set it as soon as the process starts, even on a later error.
	process *adapterprocess.Process
	// unreachableProxy reports a proxy whose bridge did not answer at startup.
	unreachableProxy bool
	// monitor is an integration the outage monitor should probe.
	monitor *integrationmonitor.Target
	// destination is a notifier the outage monitor should send events to.
	destination *integrationmonitor.Destination
}

// adapterBuilder is the runtime half of an implementation.
type adapterBuilder struct {
	// check validates the profile without network, store, or credential use.
	check func(adapterBuild) error
	// build constructs and, where needed, starts the adapter.
	build func(adapterBuild) (builtAdapter, error)
}

var adapterBuilders = map[string]adapterBuilder{}

// registerAdapter registers an implementation's config half and runtime half.
// It is called from init() in the implementation's own file.
func registerAdapter(implementation config.Implementation, builder adapterBuilder) {
	if builder.check == nil || builder.build == nil {
		panic("controllerruntime: adapter " + implementation.Name + " needs check and build")
	}
	if _, exists := adapterBuilders[implementation.Name]; exists {
		panic("controllerruntime: duplicate adapter " + implementation.Name)
	}
	config.RegisterImplementation(implementation)
	adapterBuilders[implementation.Name] = builder
}

// credentialResolver is the subset of credential.Resolver adapters use.
type credentialResolver interface {
	WithCredential(reference string, use func([]byte) error) error
}

// unavailableCredentials lets Validate construct adapters without reading any
// credential: every lookup fails.
type unavailableCredentials struct{}

func (unavailableCredentials) WithCredential(string, func([]byte) error) error {
	return errors.New("credentials are not read during validation")
}
