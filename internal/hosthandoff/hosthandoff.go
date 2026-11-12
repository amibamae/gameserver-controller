// Package hosthandoff enforces the single-use SSH host key handoff in
// protocol/adapters/hosting.md section 6.1. It decides, step by step, whether
// the controller may trust a new instance's host key and send it a secret.
// It performs no I/O; callers run ssh and record the result of each step.
package hosthandoff

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/sshkey"
)

// State is the durable handoff state.
type State string

const (
	PendingRotation State = "pending_rotation"
	Rotated         State = "rotated"
)

// DefaultLifetime is how long the single-use key may be used after creation.
const DefaultLifetime = 24 * time.Hour

// Step results, as in the conformance vector.
const (
	Permitted          = "permitted"
	HostKeyMismatch    = "host_key_mismatch"
	Expired            = "expired"
	NotPending         = "not_pending"
	NotVerified        = "not_verified"
	InvalidOutput      = "invalid_output"
	ReusedOneTimeKey   = "reused_one_time_key"
	Pinned             = "pinned"
	Replaced           = "replaced"
	NotReplaced        = "not_replaced"
	NotRotated         = "not_rotated"
	Refused            = "refused"
	sessionNone        = ""
	sessionOneTime     = "one_time"
	sessionPinned      = "pinned"
	maxRotationOutput  = 4096
	errMessageMismatch = "the presented SSH host key is not the expected key"
)

// ErrHostKeyMismatch is returned to callers that need an error value.
var ErrHostKeyMismatch = errors.New(errMessageMismatch)

// Handoff is one instance's host key handoff. The exported fields are the
// durable record; the verified session is in memory only, so a restarted
// controller must connect and verify again before sending anything.
type Handoff struct {
	OneTimeHostKey   string    `json:"one_time_host_key"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	State            State     `json:"state"`
	PinnedHostKey    string    `json:"pinned_host_key,omitempty"`
	UserDataReplaced bool      `json:"user_data_replaced"`
	session          string
}

// New starts a handoff for a freshly generated single-use host key.
func New(oneTimeHostKey ed25519.PublicKey, createdAt time.Time, lifetime time.Duration) Handoff {
	if lifetime <= 0 {
		lifetime = DefaultLifetime
	}
	return Handoff{OneTimeHostKey: sshkey.AuthorizedKey(oneTimeHostKey), CreatedAt: createdAt.UTC(),
		ExpiresAt: createdAt.UTC().Add(lifetime), State: PendingRotation}
}

// Validate checks a loaded record.
func (h Handoff) Validate() error {
	if _, err := sshkey.ParseAuthorizedKey(h.OneTimeHostKey); err != nil || h.CreatedAt.IsZero() || !h.ExpiresAt.After(h.CreatedAt) {
		return errors.New("invalid host key handoff record")
	}
	switch h.State {
	case PendingRotation:
		if h.PinnedHostKey != "" || h.UserDataReplaced {
			return errors.New("invalid host key handoff record")
		}
	case Rotated:
		if _, err := sshkey.ParseAuthorizedKey(h.PinnedHostKey); err != nil || h.PinnedHostKey == h.OneTimeHostKey {
			return errors.New("invalid host key handoff record")
		}
	default:
		return errors.New("invalid host key handoff record")
	}
	return nil
}

// ConnectOneTime records an SSH connection that presented presented at at,
// for the swap-only account. Only the stored single-use key, before expiry and
// before rotation, is accepted.
func (h *Handoff) ConnectOneTime(presented string, at time.Time) string {
	h.session = sessionNone
	switch {
	case h.State != PendingRotation:
		return NotPending
	case !at.Before(h.ExpiresAt):
		return Expired
	case !sameKey(presented, h.OneTimeHostKey):
		return HostKeyMismatch
	}
	h.session = sessionOneTime
	return Permitted
}

// Rotate records the swap command's output from a verified single-use
// connection. It pins the new key when the output is exactly one ed25519
// public key that differs from the single-use key.
func (h *Handoff) Rotate(output string) string {
	verified := h.session == sessionOneTime
	h.session = sessionNone // the swap ends the session and removes the account
	if !verified {
		return NotVerified
	}
	key, ok := RotationKey(output)
	if !ok {
		return InvalidOutput
	}
	if key == h.OneTimeHostKey {
		return ReusedOneTimeKey
	}
	h.State, h.PinnedHostKey = Rotated, key
	return Pinned
}

// ReplaceUserData records whether the provider's copy was replaced.
func (h *Handoff) ReplaceUserData(succeeded bool) string {
	if h.State != Rotated {
		return NotRotated
	}
	if !succeeded {
		return NotReplaced
	}
	h.UserDataReplaced = true
	return Replaced
}

// ConnectPinned records a later connection; only the pinned key is accepted.
func (h *Handoff) ConnectPinned(presented string) string {
	h.session = sessionNone
	if h.State != Rotated {
		return NotRotated
	}
	if !sameKey(presented, h.PinnedHostKey) {
		return HostKeyMismatch
	}
	h.session = sessionPinned
	return Permitted
}

// SendSecret reports whether a secret may be sent on the current connection:
// only when the latest connection presented the pinned key.
func (h *Handoff) SendSecret() string {
	if h.session == sessionPinned {
		return Permitted
	}
	return Refused
}

// RotationKey parses the swap command's output: exactly one canonical
// "ssh-ed25519 <base64>" line with no comment, optionally newline-terminated.
func RotationKey(output string) (string, bool) {
	if len(output) > maxRotationOutput {
		return "", false
	}
	text := strings.TrimSuffix(output, "\n")
	if strings.ContainsAny(text, "\r\n") || strings.Count(text, " ") != 1 {
		return "", false
	}
	canonical, err := sshkey.CanonicalAuthorizedKey(text)
	if err != nil || canonical != text {
		return "", false
	}
	return canonical, true
}

// sameKey compares two keys by their canonical form, ignoring comments.
func sameKey(presented, expected string) bool {
	left, err1 := sshkey.CanonicalAuthorizedKey(presented)
	right, err2 := sshkey.CanonicalAuthorizedKey(expected)
	return err1 == nil && err2 == nil && left == right
}
