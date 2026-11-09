package vultrapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const maximumStartupScriptBytes = 1 << 20

// StartupScript is metadata returned by Vultr. Script is the provider's
// base64-encoded representation; callers should avoid printing it because it
// may contain operator-specific bootstrap details.
type StartupScript struct {
	ID           string `json:"id"`
	DateCreated  string `json:"date_created,omitempty"`
	DateModified string `json:"date_modified,omitempty"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Script       string `json:"script"`
}

// StartupScriptRequest contains plaintext source in memory. The client
// base64-encodes it for Vultr's API and never retries create or update calls.
type StartupScriptRequest struct {
	Name   string
	Type   string
	Script []byte
}

type startupScriptEnvelope struct {
	StartupScript StartupScript `json:"startup_script"`
}

// GetStartupScript reads one exact startup-script ID so callers can reconcile
// a create or update whose response was lost before considering a retry.
func (c *Client) GetStartupScript(ctx context.Context, token []byte, scriptID string) (StartupScript, error) {
	if !validResourceID(scriptID) {
		return StartupScript{}, fmt.Errorf("%w: invalid startup script ID", ErrInvalidRequest)
	}
	response, err := c.request(ctx, token, "GET", "startup-scripts/"+scriptID, nil, nil, false)
	if err != nil {
		return StartupScript{}, err
	}
	var envelope startupScriptEnvelope
	if err := decodeJSON(response, &envelope); err != nil || envelope.StartupScript.ID != scriptID ||
		!validShortField(envelope.StartupScript.Name, 255) ||
		(envelope.StartupScript.Type != "boot" && envelope.StartupScript.Type != "pxe") {
		return StartupScript{}, ErrInvalidResponse
	}
	if _, err := base64.StdEncoding.DecodeString(envelope.StartupScript.Script); err != nil {
		return StartupScript{}, ErrInvalidResponse
	}
	return envelope.StartupScript, nil
}

// CreateStartupScript creates one Vultr startup script. The caller must
// reconcile an ErrOutcomeUnknown by inspecting the provider account before
// attempting another create.
func (c *Client) CreateStartupScript(ctx context.Context, token []byte, input StartupScriptRequest) (StartupScript, error) {
	if err := validateStartupScriptRequest(input); err != nil {
		return StartupScript{}, err
	}
	encoded := base64.StdEncoding.EncodeToString(input.Script)
	body, err := json.Marshal(struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Script string `json:"script"`
	}{Name: input.Name, Type: normalizedStartupScriptType(input.Type), Script: encoded})
	if err != nil {
		return StartupScript{}, fmt.Errorf("%w: could not encode startup script request", ErrInvalidRequest)
	}
	response, err := c.request(ctx, token, "POST", "startup-scripts", nil, body, true)
	if err != nil {
		return StartupScript{}, err
	}
	var envelope startupScriptEnvelope
	if err := decodeJSON(response, &envelope); err != nil || !validResourceID(envelope.StartupScript.ID) ||
		envelope.StartupScript.Name != input.Name || envelope.StartupScript.Type != normalizedStartupScriptType(input.Type) {
		return StartupScript{}, ErrOutcomeUnknown
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.StartupScript.Script)
	if err != nil || string(decoded) != string(input.Script) {
		return StartupScript{}, ErrOutcomeUnknown
	}
	return envelope.StartupScript, nil
}

// UpdateStartupScript changes one exact provider startup-script ID. It sends
// one PATCH and does not retry when the result is unclear.
func (c *Client) UpdateStartupScript(ctx context.Context, token []byte, scriptID string, input StartupScriptRequest) error {
	if !validResourceID(scriptID) {
		return fmt.Errorf("%w: invalid startup script ID", ErrInvalidRequest)
	}
	if err := validateStartupScriptRequest(input); err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Script string `json:"script"`
	}{Name: input.Name, Type: normalizedStartupScriptType(input.Type), Script: base64.StdEncoding.EncodeToString(input.Script)})
	if err != nil {
		return fmt.Errorf("%w: could not encode startup script request", ErrInvalidRequest)
	}
	_, err = c.request(ctx, token, "PATCH", "startup-scripts/"+scriptID, nil, body, true)
	return err
}

func validateStartupScriptRequest(input StartupScriptRequest) error {
	if !validShortField(input.Name, 255) {
		return fmt.Errorf("%w: startup script name is required and must be at most 255 characters", ErrInvalidRequest)
	}
	if input.Type != "" && input.Type != "boot" && input.Type != "pxe" {
		return fmt.Errorf("%w: startup script type must be boot or pxe", ErrInvalidRequest)
	}
	if len(input.Script) == 0 || len(input.Script) > maximumStartupScriptBytes || !utf8.Valid(input.Script) || bytes.IndexByte(input.Script, 0) >= 0 {
		return fmt.Errorf("%w: startup script must be non-empty UTF-8 text no larger than 1 MiB", ErrInvalidRequest)
	}
	return nil
}

func normalizedStartupScriptType(value string) string {
	if value == "" {
		return "boot"
	}
	return value
}
