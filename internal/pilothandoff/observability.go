package pilothandoff

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
)

// VictoriaLogsConfig controls optional lifecycle-only log forwarding.
// Its secret is delivered on pinned SSH stdin, never command arguments.
type VictoriaLogsConfig struct {
	OperationID, VultrCredentialRef, IngestCredentialRef string
	Endpoint, PrivacyMode                                string
}

type victoriaLogsPayload struct {
	Endpoint      string `json:"endpoint"`
	EnvironmentID string `json:"environment_id"`
	InstanceID    string `json:"instance_id"`
	PrivacyMode   string `json:"privacy_mode"`
	Token         string `json:"token"`
}

var logTokenPattern = regexp.MustCompile(`^[!-~]{16,1024}$`)

// ConfigureVictoriaLogs delivers privacy settings and a private ingress token
// after the current VM host key has been pinned and the firewall finalized.
func (r Runner) ConfigureVictoriaLogs(ctx context.Context, cfg VictoriaLogsConfig) error {
	if err := validateVictoriaLogsConfig(cfg); err != nil {
		return err
	}
	reservation, record, err := r.loadState(cfg.OperationID)
	if err != nil {
		return err
	}
	if record.State != hosthandoff.Rotated || !record.BootstrapReady || !record.SecurityFinalized {
		return errors.New("observability requires a completed pinned SSH host-key and security handoff")
	}
	settings := withDefaults(Config{VultrCredentialRef: cfg.VultrCredentialRef})
	ip, err := r.instanceAddress(ctx, settings, reservation.ProviderInstanceID, reservation.ResourceKey)
	if err != nil {
		return err
	}
	payload := victoriaLogsPayload{
		Endpoint: cfg.Endpoint, EnvironmentID: reservation.EnvironmentID,
		InstanceID: reservation.ProviderInstanceID, PrivacyMode: cfg.PrivacyMode,
	}
	admin := settings.Target(ip, settings.Remote.AdminUser)
	if cfg.PrivacyMode == "disabled" {
		return r.sendVictoriaLogs(ctx, admin, &record.Handoff, payload)
	}
	return r.sendVictoriaLogsCredential(ctx, admin, &record.Handoff, cfg.IngestCredentialRef, payload)
}

func validateVictoriaLogsConfig(cfg VictoriaLogsConfig) error {
	if cfg.PrivacyMode != "lifecycle" && cfg.PrivacyMode != "disabled" {
		return errors.New("observability privacy mode must be lifecycle or disabled")
	}
	if cfg.PrivacyMode == "lifecycle" && !validVictoriaLogsEndpoint(cfg.Endpoint) {
		return errors.New("observability endpoint must be HTTPS /insert/jsonline")
	}
	if cfg.PrivacyMode == "disabled" && cfg.Endpoint != "" {
		return errors.New("disabled observability must not specify an endpoint")
	}
	return nil
}

func (r Runner) sendVictoriaLogsCredential(ctx context.Context, target Target, handoff *hosthandoff.Handoff, ref string, payload victoriaLogsPayload) error {
	if r.Credentials == nil {
		return errors.New("observability credential resolver is required")
	}
	return r.Credentials.WithCredential(ref, func(token []byte) error {
		if !logTokenPattern.Match(token) {
			return errors.New("observability token must have 16-1024 printable non-space characters")
		}
		payload.Token = string(token)
		return r.sendVictoriaLogs(ctx, target, handoff, payload)
	})
}

func (r Runner) sendVictoriaLogs(ctx context.Context, target Target, handoff *hosthandoff.Handoff, payload victoriaLogsPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return errors.New("observability request encoding failed")
	}
	defer clear(data)
	result, err := r.runPinned(ctx, target, handoff,
		"sudo -n /usr/local/sbin/gsc-configure-victorialogs", data)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		// Remote stderr can contain a URL or future credentials.
		return errors.New("VM observability configuration failed; check the restricted remote helper")
	}
	return nil
}

func validVictoriaLogsEndpoint(value string) bool {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\t ") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return false
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Path == "/insert/jsonline" && parsed.Opaque == ""
}
