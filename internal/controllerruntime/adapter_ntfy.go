package controllerruntime

import (
	"errors"
	"net/http"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/notification"
)

// ntfy push notifier (`implementation: ntfy`). The access token goes in
// secret_refs as access_token.

const ntfyTokenSecret = "access_token"

// NtfySettings is the `ntfy:` block.
type NtfySettings struct {
	NotifierSettings `yaml:",inline"`
	ServerURL        string `yaml:"server_url"`
	Topic            string `yaml:"topic"`
}

func (n *NtfySettings) Validate(check config.SettingsCheck) {
	n.validate(check)
	check.Require(notification.ValidNtfyServer(n.ServerURL), "server_url must be an https URL, or http to a loopback address, with no path, query, or user information")
	check.Require(notification.ValidNtfyTopic(n.Topic), "topic must be 1 to 64 letters, digits, - or _")
}

func init() {
	registerAdapter(config.Implementation{
		Name: "ntfy", Category: "integration", Secrets: []string{ntfyTokenSecret},
		NewSettings: func() config.ImplementationSettings { return &NtfySettings{} },
	}, notifierBuilder(func(b adapterBuild, credentials credentialResolver) (builtAdapter, error) {
		n, ok := config.SettingsOf[*NtfySettings](b.profile)
		if !ok {
			return builtAdapter{}, errors.New("ntfy settings are missing")
		}
		sender := notification.Ntfy{ServerURL: n.ServerURL, Topic: n.Topic, Client: &http.Client{Timeout: n.timeout()}}
		return buildNotifier(b, &n.NotifierSettings, ntfyTokenSecret, sender, credentials)
	}))
}
