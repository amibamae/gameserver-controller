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

// VictoriaLogsConfig installs or disables the optional sanitized VM log shipper.
// Its credential is transported on pinned SSH stdin, never passed as an arg.
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

// ConfigureVictoriaLogs delivers a private credential and privacy settings
// only after the pilot's new host key was rotated, pinned, and verified.
// The endpoint must be an authenticated HTTPS ingestion reverse proxy.
// This method does not activate the timer until the remote helper validates
// and installs both private files.
func (r Runner) ConfigureVictoriaLogs(ctx context.Context, cfg VictoriaLogsConfig) error {
	if cfg.PrivacyMode != "lifecycle" && cfg.PrivacyMode != "disabled" {
		return errors.New("observability privacy mode must be lifecycle or disabled")
	}
	if cfg.PrivacyMode == "lifecycle" && !validVictoriaLogsEndpoint(cfg.Endpoint) {
		return errors.New("observability endpoint must be HTTPS /insert/jsonline")
	}
	if cfg.PrivacyMode == "disabled" && cfg.Endpoint != "" {
		return errors.New("disabled observability must not specify an endpoint")
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
	if r.Credentials == nil {
		return errors.New("observability credential resolver is required")
	}
	return r.Credentials.WithCredential(cfg.IngestCredentialRef, func(token []byte) error {
		if !logTokenPattern.Match(token) {
			return errors.New("observability token must have 16-1024 printable non-space characters")
		}
		payload.Token = string(token)
		return r.sendVictoriaLogs(ctx, admin, &record.Handoff, payload)
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
		// Remote stderr must never enter audit/error text: it may contain a
		// backend error, URL, or in future versions even a credential.
		return errors.New("VM observability configuration failed; check the restricted remote helper")
	}
	return nil
}

func validVictoriaLogsEndpoint(value string) bool {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\r\n\t ") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" &&
		parsed.Path == "/insert/jsonline" && parsed.Opaque == ""
}
