package config

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
)

type Controller struct {
	Mode          string        `yaml:"mode"`
	StateStore    StateStore    `yaml:"state_store"`
	Defaults      Defaults      `yaml:"defaults"`
	Audit         Audit         `yaml:"audit"`
	ManagementAPI ManagementAPI `yaml:"management_api"`
	Monitoring    Monitoring    `yaml:"monitoring"`
	// IdempotencyKeys are the installation secrets that bind management API
	// idempotency keys to actors and requests. Required when the management
	// API is enabled.
	IdempotencyKeys IdempotencyKeys `yaml:"idempotency_keys"`
}

// IdempotencyKeys names the active key version and every version still
// needed to verify retained idempotency records. Each value is a systemd
// credential reference to at least 32 bytes of random key material.
type IdempotencyKeys struct {
	Active   string            `yaml:"active"`
	Versions map[string]string `yaml:"versions"`
}

var (
	idempotencyVersionPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	idempotencyCredentialPattern = regexp.MustCompile(`^systemd-credential://[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

func validateIdempotencyKeys(problems *[]string, keys IdempotencyKeys, required bool) {
	if keys.Active == "" && len(keys.Versions) == 0 {
		if required {
			*problems = append(*problems, "controller.idempotency_keys is required when the management API is enabled")
		}
		return
	}
	if !idempotencyVersionPattern.MatchString(keys.Active) {
		*problems = append(*problems, "controller.idempotency_keys.active must be a version name")
	}
	if _, ok := keys.Versions[keys.Active]; !ok {
		*problems = append(*problems, "controller.idempotency_keys.versions must include the active version")
	}
	for version, reference := range keys.Versions {
		if !idempotencyVersionPattern.MatchString(version) || !idempotencyCredentialPattern.MatchString(reference) {
			*problems = append(*problems, "controller.idempotency_keys.versions must map version names to systemd-credential:// references")
		}
	}
}

// Monitoring configures background integration monitoring. Every field is
// optional; ProxyPolicy fills defaults.
type Monitoring struct {
	Proxy ProxyMonitoring `yaml:"proxy"`
}

// ProxyMonitoring sets how often proxy bridges are probed and when an outage
// is declared (integrations section 7).
type ProxyMonitoring struct {
	CheckInterval    string `yaml:"check_interval"`
	FailureThreshold *int   `yaml:"failure_threshold"`
	InitialBackoff   string `yaml:"initial_backoff"`
	MaxBackoff       string `yaml:"max_backoff"`
}

// ProxyPolicy returns the proxy monitoring policy with defaults applied. It
// returns an error for a value that does not parse or is out of range.
func (m Monitoring) ProxyPolicy() (integrationmonitor.Policy, error) {
	policy := integrationmonitor.DefaultPolicy
	for _, field := range []struct {
		name, text string
		target     *time.Duration
	}{
		{"check_interval", m.Proxy.CheckInterval, &policy.CheckInterval},
		{"initial_backoff", m.Proxy.InitialBackoff, &policy.InitialBackoff},
		{"max_backoff", m.Proxy.MaxBackoff, &policy.MaxBackoff},
	} {
		if field.text == "" {
			continue
		}
		parsed, err := time.ParseDuration(field.text)
		if err != nil {
			return policy, fmt.Errorf("%s must be a duration", field.name)
		}
		*field.target = parsed
	}
	if m.Proxy.FailureThreshold != nil {
		policy.FailureThreshold = *m.Proxy.FailureThreshold
	}
	return policy, policy.Validate()
}

// ManagementAPI configures the optional remote HTTPS management listener.
// Disabled is the zero value, so existing configurations remain offline.
type ManagementAPI struct {
	Enabled        bool   `yaml:"enabled"`
	ListenAddress  string `yaml:"listen_address"`
	Audience       string `yaml:"audience"`
	TLSCertificate string `yaml:"tls_certificate_file"`
	TLSPrivateKey  string `yaml:"tls_private_key_file"`
}

type StateStore struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

type Defaults struct {
	OperationTimeout string `yaml:"operation_timeout"`
	RetryLimit       *int   `yaml:"retry_limit"`
}

type Audit struct {
	Enabled     bool   `yaml:"enabled"`
	Destination string `yaml:"destination"`
}

func validateManagementAPI(problems *[]string, value ManagementAPI) {
	if !value.Enabled {
		return
	}
	host, portText, err := net.SplitHostPort(value.ListenAddress)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || net.ParseIP(host) == nil || portErr != nil || port < 1 || port > 65535 {
		*problems = append(*problems, "controller.management_api.listen_address must be an IP address and TCP port")
	}
	if !identifierPattern.MatchString(value.Audience) {
		*problems = append(*problems, "controller.management_api.audience must be a lower-case identifier")
	}
	for field, path := range map[string]string{
		"tls_certificate_file": value.TLSCertificate,
		"tls_private_key_file": value.TLSPrivateKey,
	} {
		if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') || strings.Contains(path, "://") || !filepath.IsAbs(path) {
			*problems = append(*problems, "controller.management_api."+field+" must be an absolute local path")
		}
	}
	if value.TLSCertificate != "" && value.TLSCertificate == value.TLSPrivateKey {
		*problems = append(*problems, "controller.management_api TLS certificate and private-key paths must differ")
	}
}

// validateController checks the controller section: local mode, the bbolt
// state store, operation defaults, local audit, the management API and its
// idempotency keys, and monitoring. A new controller setting adds its check
// here, beside its type.
func validateController(problems *[]string, value Controller) {
	add := func(ok bool, message string) {
		if !ok {
			*problems = append(*problems, message)
		}
	}
	add(value.Mode == "local", "controller.mode must be local")
	add(value.StateStore.Type == "bbolt", "controller.state_store.type must be bbolt")
	statePath := strings.TrimSpace(value.StateStore.Path)
	add(statePath != "" && !strings.ContainsRune(statePath, '\x00') && !strings.Contains(statePath, "://"), "controller.state_store.path must be a non-empty local path")
	timeout, err := time.ParseDuration(value.Defaults.OperationTimeout)
	add(err == nil && timeout > 0 && timeout <= 24*time.Hour, "controller.defaults.operation_timeout must be greater than zero and no more than 24h")
	add(value.Defaults.RetryLimit != nil && *value.Defaults.RetryLimit >= 0 && *value.Defaults.RetryLimit <= 10, "controller.defaults.retry_limit must be explicitly set between 0 and 10")
	add(value.Audit.Enabled, "controller.audit.enabled must be true")
	add(value.Audit.Destination == "local", "controller.audit.destination must be local")
	validateManagementAPI(problems, value.ManagementAPI)
	validateIdempotencyKeys(problems, value.IdempotencyKeys, value.ManagementAPI.Enabled)
	if _, err := value.Monitoring.ProxyPolicy(); err != nil {
		add(false, "controller.monitoring.proxy."+err.Error())
	}
}
