package controllerruntime

import (
	"fmt"
	"slices"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
	"github.com/amibamae/gameserver-controller/internal/notification"
)

// Shared parts of the notifier implementations. Each notifier lives in its
// own adapter_<name>.go file (webhook, smtp, ntfy); deleting that file removes
// it. The controller log channel is built into integrationmonitor and needs no
// adapter.

// notifierEvents are the event types a notifier's `events` filter may list.
var notifierEvents = []string{
	integrationmonitor.EventUnreachable,
	integrationmonitor.EventIdentityMismatch,
	integrationmonitor.EventRecovered,
}

// NotifierSettings are the fields every notifier block shares.
type NotifierSettings struct {
	EnvironmentID string   `yaml:"environment_id"`
	Events        []string `yaml:"events"`
	Timeout       string   `yaml:"timeout"`
}

func (n *NotifierSettings) BoundEnvironment() string { return n.EnvironmentID }

func (n *NotifierSettings) validate(check config.SettingsCheck) {
	check.Require(check.KnownEnvironment(n.EnvironmentID), "environment_id must name a configured environment")
	seen := map[string]bool{}
	for _, event := range n.Events {
		check.Require(slices.Contains(notifierEvents, event), fmt.Sprintf("events must list only %v", notifierEvents))
		check.Require(!seen[event], "events must not repeat "+event)
		seen[event] = true
	}
	if n.Timeout != "" {
		timeout, err := time.ParseDuration(n.Timeout)
		check.Require(err == nil && timeout > 0 && timeout <= time.Minute, "timeout must be greater than zero and no more than 1m")
	}
}

func (n *NotifierSettings) timeout() time.Duration {
	timeout, err := time.ParseDuration(n.Timeout)
	if err != nil || timeout <= 0 {
		return 20 * time.Second
	}
	return timeout
}

// buildNotifier builds one notifier adapter and the destination the
// dispatcher sends to. It contacts nothing.
func buildNotifier(b adapterBuild, settings *NotifierSettings, secretName string, sender notification.Sender, credentials credentialResolver) (builtAdapter, error) {
	secret := b.profile.SecretRefs[secretName]
	notifier, err := notification.New(notification.Config{
		AdapterID: b.profile.ID, EnvironmentID: settings.EnvironmentID, SecretRef: secret.SecretRef,
		Sender: sender, Credentials: credentials,
	}, b.protocolVersion)
	if err != nil {
		return builtAdapter{}, err
	}
	return builtAdapter{implementation: notifier, destination: &integrationmonitor.Destination{
		AdapterID: b.profile.ID, EnvironmentID: settings.EnvironmentID, Events: settings.Events,
		SecretRefs: []adapter.SecretReference{{SecretRef: secret.SecretRef, Purpose: secret.Purpose}},
	}}, nil
}

// notifierBuilder wires check and build for a notifier implementation.
func notifierBuilder(make func(b adapterBuild, credentials credentialResolver) (builtAdapter, error)) adapterBuilder {
	return adapterBuilder{
		check: func(b adapterBuild) error {
			_, err := make(b, unavailableCredentials{})
			return err
		},
		build: func(b adapterBuild) (builtAdapter, error) {
			credentials, err := b.credentials()
			if err != nil {
				return builtAdapter{}, err
			}
			return make(b, credentials)
		},
	}
}
