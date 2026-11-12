package controllerruntime

import (
	"errors"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/notification"
)

// SMTP email notifier (`implementation: smtp`). The SMTP password goes in
// secret_refs as password. Only authenticated submission over TLS is offered.

const smtpPasswordSecret = "password"

// SMTPSettings is the `smtp:` block.
type SMTPSettings struct {
	NotifierSettings `yaml:",inline"`
	Host             string   `yaml:"host"`
	Port             int      `yaml:"port"`
	TLS              string   `yaml:"tls"`
	Username         string   `yaml:"username"`
	From             string   `yaml:"from"`
	To               []string `yaml:"to"`
}

func (s *SMTPSettings) Validate(check config.SettingsCheck) {
	s.validate(check)
	check.Require(config.ValidDNSHostname(s.Host), "host must be a DNS hostname")
	check.Require(s.Port >= 1 && s.Port <= 65535, "port must be between 1 and 65535")
	check.Require(s.TLS == notification.TLSStartTLS || s.TLS == notification.TLSImplicit, "tls must be starttls or implicit")
	check.Require(s.Username != "" && len(s.Username) <= 320, "username is required")
	check.Require(notification.ValidAddress(s.From), "from must be one email address without a display name")
	check.Require(len(s.To) > 0 && len(s.To) <= 20, "to must list 1 to 20 addresses")
	for _, to := range s.To {
		check.Require(notification.ValidAddress(to), "to must list email addresses without display names")
	}
}

func init() {
	registerAdapter(config.Implementation{
		Name: "smtp", Category: "integration", Secrets: []string{smtpPasswordSecret},
		NewSettings: func() config.ImplementationSettings { return &SMTPSettings{} },
	}, notifierBuilder(func(b adapterBuild, credentials credentialResolver) (builtAdapter, error) {
		s, ok := config.SettingsOf[*SMTPSettings](b.profile)
		if !ok {
			return builtAdapter{}, errors.New("smtp settings are missing")
		}
		sender := notification.SMTP{Host: s.Host, Port: s.Port, TLSMode: s.TLS, Username: s.Username, From: s.From, To: s.To, Timeout: s.timeout()}
		return buildNotifier(b, &s.NotifierSettings, smtpPasswordSecret, sender, credentials)
	}))
}
