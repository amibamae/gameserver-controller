// Package pilothandoff finishes setting up a new pilot VM: it verifies the
// VM by its single-use SSH host key, swaps that key and pins the new one
// (protocol hosting section 6.1), then sends the Wasabi restore credentials
// only to the pinned key and starts the restore.
package pilothandoff

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

var (
	ErrNoHandoff = errors.New("this VM has no single-use host key record; it was provisioned before the host key handoff existed, so finish it with the manual process")
	ErrNotActive = errors.New("the capacity reservation is not a confirmed, present VM")
	ErrHostKey   = errors.New("SSH host key mismatch: the VM did not present the expected host key, so nothing was sent")
	ErrExpired   = errors.New("the single-use host key handoff has expired; verify the VM's fingerprint in the Vultr web console, or destroy and recreate it")
	ErrRotation  = errors.New("the host key swap did not return one valid new key")
	ErrRestore   = errors.New("minecraft-restore.service failed on the VM; inspect it before retrying")
	ErrTimedOut  = errors.New("the handoff did not finish before the timeout; run it again with the same operation ID to resume")

	accessKeyPattern = regexp.MustCompile(`^[A-Za-z0-9+/=]{16,128}$`)
	regionPattern    = regexp.MustCompile(`^[a-z]{2}-[a-z]+-[0-9]{1,2}$`)
)

// mismatchHint explains the one case a mismatch is expected: an earlier run
// lost the swap's output after the VM had already swapped its key.
const mismatchHint = "If an earlier handoff run was interrupted during the key swap, the VM may already have a new key the controller never received; compare the fingerprint the VM wrote to its Vultr web console. Otherwise treat it as a possible impostor."

// Store is the subset of the pilot state store the handoff uses.
type Store interface {
	GetCapacityReservation(string) (store.CapacityReservation, error)
	GetHostHandoff(string) (store.HostHandoffRecord, error)
	UpdateHostHandoff(string, func(*store.HostHandoffRecord) error) (store.HostHandoffRecord, error)
}

// Provider is the subset of the Vultr client the handoff uses.
type Provider interface {
	GetInstance(context.Context, []byte, string) (vultrapi.Instance, error)
	ReplaceInstanceUserData(context.Context, []byte, string, []byte) error
}

// Credentials reads systemd credentials.
type Credentials interface {
	WithCredential(reference string, use func([]byte) error) error
}

// Remote holds the VM-side commands. The defaults match the bootstrap.
type Remote struct {
	HostKeyUser, AdminUser string
	BootstrapReady         string
	WriteRcloneConfig      string
	Finalize               string
	Install                string
	RestoreStatus          string
}

// DefaultRemote is the bootstrap's layout.
var DefaultRemote = Remote{
	HostKeyUser:       "gsc-hostkey",
	AdminUser:         "minecraft-admin",
	BootstrapReady:    "test -f /var/lib/minecraft-bootstrap/bootstrap-complete",
	WriteRcloneConfig: "umask 077 && cat > /home/minecraft-admin/rclone.conf.tmp && mv -f /home/minecraft-admin/rclone.conf.tmp /home/minecraft-admin/rclone.conf",
	Finalize:          "sudo -n /usr/local/sbin/minecraft-finalize-security",
	Install:           "sudo -n /usr/local/sbin/minecraft-install-rclone-config",
	RestoreStatus:     "sudo -n /usr/bin/systemctl is-active minecraft-restore.service",
}

// Config is one handoff run.
type Config struct {
	OperationID        string
	VultrCredentialRef string
	WasabiAccessKeyRef string
	WasabiSecretKeyRef string
	WasabiRegion       string
	Remote             Remote
	// Target maps a login to an address; the default is port 22 on the VM's
	// provider-reported IP. Tests point it at local servers.
	Target       func(ip netip.Addr, user string) Target
	PollInterval time.Duration
	Now          func() time.Time
	Sleep        func(context.Context, time.Duration) bool
	// Progress receives one line per step. It never receives secrets.
	Progress func(string)
}

// Report summarizes a finished handoff.
type Report struct {
	InstanceID        string
	Address           netip.Addr
	PinnedFingerprint string
	UserDataReplaced  bool
	RestoreComplete   bool
}

// Runner performs handoffs.
type Runner struct {
	Store       Store
	Provider    Provider
	Credentials Credentials
	SSH         SSH
}

// Run performs or resumes one handoff. It returns when the restore has
// finished, on a hard stop, or when ctx ends.
func (r Runner) Run(ctx context.Context, config Config) (Report, error) {
	config = withDefaults(config)
	if !regionPattern.MatchString(config.WasabiRegion) {
		return Report{}, errors.New("--wasabi-region must be a Wasabi region such as us-east-1")
	}
	reservation, err := r.Store.GetCapacityReservation(config.OperationID)
	if err != nil {
		return Report{}, err
	}
	if reservation.State != store.CapacityActive || reservation.ProviderInstanceID == "" {
		return Report{}, ErrNotActive
	}
	record, err := r.Store.GetHostHandoff(config.OperationID)
	if errors.Is(err, store.ErrHostHandoffNotFound) {
		return Report{}, ErrNoHandoff
	}
	if err != nil {
		return Report{}, err
	}
	instanceID := reservation.ProviderInstanceID
	if record.InstanceID != "" && record.InstanceID != instanceID {
		return Report{}, errors.New("the handoff record names a different instance")
	}
	ip, err := r.instanceAddress(ctx, config, instanceID, reservation.ResourceKey)
	if err != nil {
		return Report{}, err
	}
	if record.InstanceID == "" {
		if record, err = r.Store.UpdateHostHandoff(config.OperationID, func(h *store.HostHandoffRecord) error {
			h.InstanceID = instanceID
			return nil
		}); err != nil {
			return Report{}, err
		}
	}
	report := Report{InstanceID: instanceID, Address: ip}
	handoff := record.Handoff

	if handoff.State == hosthandoff.PendingRotation {
		if err := r.rotate(ctx, config, ip, &handoff); err != nil {
			return report, err
		}
		if record, err = r.Store.UpdateHostHandoff(config.OperationID, func(h *store.HostHandoffRecord) error {
			h.State, h.PinnedHostKey = handoff.State, handoff.PinnedHostKey
			return nil
		}); err != nil {
			return report, fmt.Errorf("the host key was swapped but pinning it failed; the new key is %s: %w", handoff.PinnedHostKey, err)
		}
	}
	report.PinnedFingerprint = fingerprint(handoff.PinnedHostKey)
	config.Progress("Pinned host key " + report.PinnedFingerprint)

	if !record.UserDataReplaced {
		replaceErr := r.withToken(config, func(token []byte) error {
			return r.Provider.ReplaceInstanceUserData(ctx, token, instanceID, []byte("#cloud-config\n{}\n"))
		})
		if handoff.ReplaceUserData(replaceErr == nil) == hosthandoff.Replaced {
			if record, err = r.Store.UpdateHostHandoff(config.OperationID, func(h *store.HostHandoffRecord) error {
				h.UserDataReplaced = true
				return nil
			}); err != nil {
				return report, err
			}
			config.Progress("Replaced the VM's user data at Vultr; the expired single-use key is gone from provider metadata.")
		} else {
			config.Progress("Warning: could not confirm that Vultr's copy of the user data was replaced; the expired single-use key may remain in provider metadata. The next run tries again.")
		}
	}
	report.UserDataReplaced = record.UserDataReplaced

	admin := config.Target(ip, config.Remote.AdminUser)
	if !record.BootstrapReady {
		config.Progress("Waiting for the bootstrap to finish...")
		if err := r.until(ctx, config, func() (bool, error) {
			result, err := r.runPinned(ctx, admin, &handoff, config.Remote.BootstrapReady, nil)
			if err != nil {
				return false, err
			}
			switch result.ExitCode {
			case 0:
				return true, nil
			case 1, 255:
				return false, nil
			}
			return false, fmt.Errorf("bootstrap check failed: %s", firstLine(result.Stderr))
		}); err != nil {
			return report, err
		}
		if record, err = r.mark(config, func(h *store.HostHandoffRecord) { h.BootstrapReady = true }); err != nil {
			return report, err
		}
	}

	if !record.CredentialsSent {
		if err := r.sendCredentials(ctx, config, admin, &handoff); err != nil {
			return report, err
		}
		if record, err = r.mark(config, func(h *store.HostHandoffRecord) { h.CredentialsSent = true }); err != nil {
			return report, err
		}
		config.Progress("Sent the Wasabi restore credentials to the pinned host.")
	}

	if !record.SecurityFinalized {
		if err := r.mustRun(ctx, admin, &handoff, config.Remote.Finalize, "security finalizer"); err != nil {
			return report, err
		}
		if record, err = r.mark(config, func(h *store.HostHandoffRecord) { h.SecurityFinalized = true }); err != nil {
			return report, err
		}
		config.Progress("Finalized SSH and firewall security on the VM.")
	}

	if !record.RestoreStarted {
		state, err := r.restoreState(ctx, admin, &handoff, config)
		if err != nil {
			return report, err
		}
		if state != "active" && state != "activating" {
			if err := r.mustRun(ctx, admin, &handoff, config.Remote.Install, "rclone installer"); err != nil {
				return report, err
			}
		}
		if record, err = r.mark(config, func(h *store.HostHandoffRecord) { h.RestoreStarted = true }); err != nil {
			return report, err
		}
		config.Progress("Started the Wasabi restore.")
	}

	config.Progress("Waiting for the restore to finish...")
	err = r.until(ctx, config, func() (bool, error) {
		state, err := r.restoreState(ctx, admin, &handoff, config)
		switch {
		case err != nil:
			return false, err
		case state == "active":
			return true, nil
		case state == "failed":
			return false, ErrRestore
		}
		return false, nil
	})
	if err != nil {
		return report, err
	}
	report.RestoreComplete = true
	return report, nil
}

// rotate verifies the VM by its single-use key and swaps it.
func (r Runner) rotate(ctx context.Context, config Config, ip netip.Addr, handoff *hosthandoff.Handoff) error {
	target := config.Target(ip, config.Remote.HostKeyUser)
	config.Progress("Waiting for the VM to present its single-use host key " + fingerprint(handoff.OneTimeHostKey) + "...")
	return r.until(ctx, config, func() (bool, error) {
		presented, err := r.SSH.HostKey(ctx, target)
		if errors.Is(err, ErrNoHostKey) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%w (%v). %s", ErrHostKey, err, mismatchHint)
		}
		switch handoff.ConnectOneTime(presented, config.Now()) {
		case hosthandoff.Permitted:
		case hosthandoff.Expired:
			return false, ErrExpired
		case hosthandoff.HostKeyMismatch:
			return false, fmt.Errorf("%w: presented %s, expected %s. %s", ErrHostKey, fingerprint(presented), fingerprint(handoff.OneTimeHostKey), mismatchHint)
		default:
			return false, errors.New("the handoff is not waiting for a key swap")
		}
		result, err := r.SSH.Run(ctx, target, handoff.OneTimeHostKey, "", nil)
		switch {
		case err != nil:
			return false, err
		case hostKeyRefused(result):
			return false, fmt.Errorf("%w. %s", ErrHostKey, mismatchHint)
		case authRefused(result), result.ExitCode == 255:
			// The swap account does not exist yet, or the connection failed
			// before the command ran.
			return false, nil
		case result.ExitCode != 0:
			return false, fmt.Errorf("the host key swap failed on the VM: %s", firstLine(result.Stderr))
		}
		if handoff.Rotate(string(result.Stdout)) != hosthandoff.Pinned {
			return false, fmt.Errorf("%w. %s", ErrRotation, mismatchHint)
		}
		return true, nil
	})
}

// runPinned runs a command only after the address presents the pinned key;
// ssh then enforces the same key for the login itself.
func (r Runner) runPinned(ctx context.Context, target Target, handoff *hosthandoff.Handoff, command string, stdin []byte) (Result, error) {
	presented, err := r.SSH.HostKey(ctx, target)
	if errors.Is(err, ErrNoHostKey) {
		return Result{ExitCode: 255}, nil
	}
	if err != nil || handoff.ConnectPinned(presented) != hosthandoff.Permitted {
		return Result{}, fmt.Errorf("%w: the VM no longer presents its pinned key %s", ErrHostKey, fingerprint(handoff.PinnedHostKey))
	}
	if stdin != nil && handoff.SendSecret() != hosthandoff.Permitted {
		return Result{}, errors.New("refusing to send a secret on a connection that is not verified by the pinned key")
	}
	result, err := r.SSH.Run(ctx, target, handoff.PinnedHostKey, command, stdin)
	if err == nil && hostKeyRefused(result) {
		return Result{}, fmt.Errorf("%w: ssh refused the VM's host key", ErrHostKey)
	}
	return result, err
}

func (r Runner) mustRun(ctx context.Context, target Target, handoff *hosthandoff.Handoff, command, name string) error {
	result, err := r.runPinned(ctx, target, handoff, command, nil)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("the %s failed on the VM: %s", name, firstLine(result.Stderr))
	}
	return nil
}

func (r Runner) restoreState(ctx context.Context, target Target, handoff *hosthandoff.Handoff, config Config) (string, error) {
	result, err := r.runPinned(ctx, target, handoff, config.Remote.RestoreStatus, nil)
	if err != nil {
		return "", err
	}
	if result.ExitCode == 255 {
		return "unreachable", nil
	}
	return strings.TrimSpace(firstLine(result.Stdout)), nil
}

func (r Runner) sendCredentials(ctx context.Context, config Config, target Target, handoff *hosthandoff.Handoff) error {
	var conf []byte
	err := r.Credentials.WithCredential(config.WasabiAccessKeyRef, func(access []byte) error {
		return r.Credentials.WithCredential(config.WasabiSecretKeyRef, func(secret []byte) error {
			var buildErr error
			conf, buildErr = RcloneConfig(access, secret, config.WasabiRegion)
			return buildErr
		})
	})
	if err != nil {
		return fmt.Errorf("the VM Wasabi credentials are missing or invalid: %w", err)
	}
	defer clear(conf)
	result, err := r.runPinned(ctx, target, handoff, config.Remote.WriteRcloneConfig, conf)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("writing the rclone configuration failed on the VM: %s", firstLine(result.Stderr))
	}
	return nil
}

// RcloneConfig renders the VM's Wasabi remote. Key values are checked so
// they cannot add lines or settings.
func RcloneConfig(accessKey, secretKey []byte, region string) ([]byte, error) {
	access, secret := strings.TrimSpace(string(accessKey)), strings.TrimSpace(string(secretKey))
	if !accessKeyPattern.MatchString(access) || !accessKeyPattern.MatchString(secret) {
		return nil, errors.New("the Wasabi access key ID and secret key must be single tokens of 16 to 128 letters, digits, +, / or =")
	}
	if !regionPattern.MatchString(region) {
		return nil, errors.New("invalid Wasabi region")
	}
	return []byte("[wasabi]\ntype = s3\nprovider = Wasabi\naccess_key_id = " + access + "\nsecret_access_key = " + secret +
		"\nregion = " + region + "\nendpoint = s3." + region + ".wasabisys.com\nacl = private\n"), nil
}

func (r Runner) instanceAddress(ctx context.Context, config Config, instanceID, label string) (netip.Addr, error) {
	var ip netip.Addr
	err := r.until(ctx, config, func() (bool, error) {
		var instance vultrapi.Instance
		err := r.withToken(config, func(token []byte) error {
			var getErr error
			instance, getErr = r.Provider.GetInstance(ctx, token, instanceID)
			return getErr
		})
		if err != nil {
			return false, fmt.Errorf("could not read the VM from Vultr: %w", err)
		}
		if instance.ID != instanceID || instance.Label != label {
			return false, errors.New("Vultr returned a different instance than the reservation names")
		}
		parsed, err := netip.ParseAddr(instance.MainIP)
		if err != nil || !parsed.Is4() || parsed.IsUnspecified() {
			return false, nil // no public IP assigned yet
		}
		if !parsed.IsGlobalUnicast() || parsed.IsPrivate() || parsed.IsLoopback() {
			return false, errors.New("Vultr reported a non-public address for the VM")
		}
		ip = parsed
		return true, nil
	})
	return ip, err
}

func (r Runner) withToken(config Config, use func([]byte) error) error {
	return r.Credentials.WithCredential(config.VultrCredentialRef, use)
}

func (r Runner) mark(config Config, change func(*store.HostHandoffRecord)) (store.HostHandoffRecord, error) {
	return r.Store.UpdateHostHandoff(config.OperationID, func(h *store.HostHandoffRecord) error {
		change(h)
		return nil
	})
}

// until polls step until it reports done, fails, or ctx ends.
func (r Runner) until(ctx context.Context, config Config, step func() (bool, error)) error {
	for {
		done, err := step()
		if err != nil || done {
			return err
		}
		if !config.Sleep(ctx, config.PollInterval) {
			return ErrTimedOut
		}
	}
}

func withDefaults(config Config) Config {
	if config.Remote == (Remote{}) {
		config.Remote = DefaultRemote
	}
	if config.Target == nil {
		config.Target = func(ip netip.Addr, user string) Target { return Target{Address: ip.String(), Port: 22, User: user} }
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 10 * time.Second
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Sleep == nil {
		config.Sleep = func(ctx context.Context, d time.Duration) bool {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-timer.C:
				return true
			}
		}
	}
	if config.Progress == nil {
		config.Progress = func(string) {}
	}
	return config
}

func fingerprint(authorizedKey string) string {
	key, err := sshkey.ParseAuthorizedKey(authorizedKey)
	if err != nil {
		return "(invalid key)"
	}
	return sshkey.Fingerprint(key)
}

// firstLine returns a short, printable first line of remote output.
func firstLine(data []byte) string {
	text := strings.TrimSpace(string(data))
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 200 {
		text = text[:200]
	}
	if !utf8.ValidString(text) {
		return "(unprintable output)"
	}
	return strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, text)
}
