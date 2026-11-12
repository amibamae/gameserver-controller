package controllerruntime

import (
	"errors"
	"net/http"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/notification"
)

// Discord, Slack, or JSON webhook notifier (`implementation: webhook`). The
// webhook URL is a secret: it goes in secret_refs as webhook_url.

const webhookURLSecret = "webhook_url"

// WebhookSettings is the `webhook:` block.
type WebhookSettings struct {
	NotifierSettings `yaml:",inline"`
	Format           string `yaml:"format"`
}

func (w *WebhookSettings) Validate(check config.SettingsCheck) {
	w.validate(check)
	switch w.Format {
	case notification.FormatDiscord, notification.FormatSlack, notification.FormatJSON:
	default:
		check.Require(false, "format must be discord, slack, or json")
	}
}

func init() {
	registerAdapter(config.Implementation{
		Name: "webhook", Category: "integration", Secrets: []string{webhookURLSecret},
		NewSettings: func() config.ImplementationSettings { return &WebhookSettings{} },
	}, notifierBuilder(func(b adapterBuild, credentials credentialResolver) (builtAdapter, error) {
		w, ok := config.SettingsOf[*WebhookSettings](b.profile)
		if !ok {
			return builtAdapter{}, errors.New("webhook settings are missing")
		}
		sender := notification.Webhook{Format: w.Format, Client: &http.Client{Timeout: w.timeout()}}
		return buildNotifier(b, &w.NotifierSettings, webhookURLSecret, sender, credentials)
	}))
}
