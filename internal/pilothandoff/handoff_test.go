package pilothandoff

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

const (
	operationID = "0123456789abcdef0123456789abcdef"
	resourceKey = "gsc-managed-test"
	instanceID  = "instance-1"
	accessKey   = "SYNTHETICACCESSKEY01"
	secretKey   = "synthetic+secret/key=0123456789abcdefgh"
)

type fakeProvider struct {
	mu       sync.Mutex
	replaced [][]byte
	fail     bool
}

func (p *fakeProvider) GetInstance(context.Context, []byte, string) (vultrapi.Instance, error) {
	return vultrapi.Instance{ID: instanceID, Label: resourceKey, MainIP: "203.0.113.20"}, nil
}

func (p *fakeProvider) ReplaceInstanceUserData(_ context.Context, _ []byte, _ string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return vultrapi.ErrOutcomeUnknown
	}
	p.replaced = append(p.replaced, append([]byte(nil), data...))
	return nil
}

type fakeCredentials map[string]string

func (c fakeCredentials) WithCredential(reference string, use func([]byte) error) error {
	value, ok := c[reference]
	if !ok {
		return errors.New("missing credential")
	}
	return use([]byte(value))
}

// keyFiles writes an ed25519 key pair and returns the private path and the
// public authorized_keys line.
func keyFiles(t *testing.T, dir, name string) (string, string, ed25519.PublicKey) {
	t.Helper()
	public, private, err := sshkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := sshkey.MarshalPrivateKey(private, name)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	line := sshkey.AuthorizedKey(public)
	if err := os.WriteFile(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, line, public
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// startSSHD runs a private sshd on 127.0.0.1 for the current user.
func startSSHD(t *testing.T, dir, name, hostKey, authorizedKeys string) int {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		if _, statErr := os.Stat("/usr/sbin/sshd"); statErr != nil {
			t.Skip("sshd is not installed")
		}
		sshd = "/usr/sbin/sshd"
	}
	if os.Geteuid() == 0 {
		_ = os.MkdirAll("/run/sshd", 0o755)
	}
	port := freePort(t)
	config := filepath.Join(dir, name+".conf")
	text := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
PermitRootLogin yes
LogLevel ERROR
`, port, hostKey, filepath.Join(dir, name+".pid"), authorizedKeys)
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshd, "-D", "-e", "-f", config)
	var logs strings.Builder
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start sshd: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return port
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("sshd did not start: %s", logs.String())
	return 0
}

type world struct {
	dir                 string
	store               *store.BoltStore
	provider            *fakeProvider
	runner              Runner
	config              Config
	oneTimeKey, swapKey string
	swapPort, adminPort int
}

// newWorld sets up a provisioned VM: a reservation with its handoff record,
// a "fresh VM" sshd presenting servedOneTime, and a "swapped VM" sshd
// presenting servedPinned while the swap script reports swapKey.
func newWorld(t *testing.T, impostorFirst, impostorAfter bool) *world {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	dir := t.TempDir()
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	clientKey, clientPublic, _ := keyFiles(t, dir, "client")
	oneTimePath, oneTimeLine, oneTimePublic := keyFiles(t, dir, "one_time_host")
	swapPath, swapLine, _ := keyFiles(t, dir, "swapped_host")
	servedFirst, servedAfter := oneTimePath, swapPath
	if impostorFirst {
		servedFirst, _, _ = keyFiles(t, dir, "impostor_first")
	}
	if impostorAfter {
		servedAfter, _, _ = keyFiles(t, dir, "impostor_after")
	}
	rotate := filepath.Join(dir, "rotate.sh")
	script := fmt.Sprintf("#!/bin/sh\necho swapped >> %s/rotations\ncut -d' ' -f1,2 %s.pub\n", dir, swapPath)
	if err := os.WriteFile(rotate, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	swapKeys := filepath.Join(dir, "authorized_keys_swap")
	if err := os.WriteFile(swapKeys, []byte(fmt.Sprintf("restrict,command=%q %s\n", rotate, clientPublic)), 0o600); err != nil {
		t.Fatal(err)
	}
	adminKeys := filepath.Join(dir, "authorized_keys_admin")
	if err := os.WriteFile(adminKeys, []byte(clientPublic+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	swapPort := startSSHD(t, dir, "swap", servedFirst, swapKeys)
	adminPort := startSSHD(t, dir, "admin", servedAfter, adminKeys)

	repository, err := store.OpenBolt(filepath.Join(dir, "pilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	if _, _, err := repository.ReserveCapacity(store.CapacityReservation{OperationID: operationID, ServerID: "pilot", EnvironmentID: "staging",
		CapacityGroup: "shared", Provider: "vultr", ResourceKey: resourceKey}, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimCapacityCreateWithHandoff(operationID, hosthandoff.New(oneTimePublic, time.Now(), 0)); err != nil {
		t.Fatal(err)
	}
	reservation, _ := repository.GetCapacityReservation(operationID)
	if _, err := repository.ObserveCapacity(operationID, store.CapacityProviderObservation{Provider: "vultr", ResourceKey: resourceKey,
		ProviderInstanceID: instanceID, State: "present", ObservedAt: reservation.CreatedAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{}
	remote := Remote{
		HostKeyUser: "swap", AdminUser: "admin",
		BootstrapReady:    "test -f " + dir + "/bootstrap-complete",
		WriteRcloneConfig: "umask 077 && cat > " + dir + "/rclone.conf",
		Finalize:          "touch " + dir + "/finalized",
		Install:           "touch " + dir + "/restore-started",
		RestoreStatus:     "if [ -f " + dir + "/restore-started ]; then echo active; else echo inactive; fi",
	}
	w := &world{dir: dir, store: repository, provider: provider, oneTimeKey: oneTimeLine, swapKey: swapLine, swapPort: swapPort, adminPort: adminPort}
	w.runner = Runner{Store: repository, Provider: provider, SSH: OpenSSH{IdentityFile: clientKey},
		Credentials: fakeCredentials{"vultr": "token", "access": accessKey, "secret": secretKey}}
	w.config = Config{
		OperationID: operationID, VultrCredentialRef: "vultr", WasabiAccessKeyRef: "access", WasabiSecretKeyRef: "secret",
		WasabiRegion: "us-east-1", Remote: remote, PollInterval: 50 * time.Millisecond,
		Target: func(_ netip.Addr, role string) Target {
			if role == "swap" {
				return Target{Address: "127.0.0.1", Port: swapPort, User: current.Username}
			}
			return Target{Address: "127.0.0.1", Port: adminPort, User: current.Username}
		},
	}
	return w
}

func (w *world) exists(name string) bool {
	_, err := os.Stat(filepath.Join(w.dir, name))
	return err == nil
}

func TestHandoffVerifiesSwapsPinsThenSendsCredentials(t *testing.T) {
	w := newWorld(t, false, false)
	if err := os.WriteFile(filepath.Join(w.dir, "bootstrap-complete"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := w.runner.Run(ctx, w.config)
	if err != nil {
		t.Fatal(err)
	}
	if !report.RestoreComplete || !report.UserDataReplaced || report.InstanceID != instanceID {
		t.Fatalf("report = %+v", report)
	}
	record, _ := w.store.GetHostHandoff(operationID)
	if record.State != hosthandoff.Rotated || record.PinnedHostKey != w.swapKey || !record.CredentialsSent || !record.SecurityFinalized || !record.RestoreStarted {
		t.Fatalf("record = %+v", record)
	}
	conf, err := os.ReadFile(filepath.Join(w.dir, "rclone.conf"))
	if err != nil || !strings.Contains(string(conf), "access_key_id = "+accessKey) || !strings.Contains(string(conf), "endpoint = s3.us-east-1.wasabisys.com") {
		t.Fatalf("rclone.conf = %q, %v", conf, err)
	}
	if len(w.provider.replaced) != 1 || strings.Contains(string(w.provider.replaced[0]), "PRIVATE") {
		t.Fatalf("user data replacement = %q", w.provider.replaced)
	}
	// A second run resumes from the record: no second swap, nothing resent.
	if err := os.Remove(filepath.Join(w.dir, "rclone.conf")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.runner.Run(ctx, w.config); err != nil {
		t.Fatal(err)
	}
	rotations, _ := os.ReadFile(filepath.Join(w.dir, "rotations"))
	if strings.Count(string(rotations), "swapped") != 1 || w.exists("rclone.conf") {
		t.Fatalf("a finished handoff repeated work: rotations=%q", rotations)
	}
}

func TestImpostorAtFirstConnectionGetsNothing(t *testing.T) {
	w := newWorld(t, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := w.runner.Run(ctx, w.config)
	if !errors.Is(err, ErrHostKey) {
		t.Fatalf("Run = %v, want a host key mismatch", err)
	}
	if w.exists("rotations") || w.exists("rclone.conf") {
		t.Fatal("the impostor ran the swap or received credentials")
	}
	record, _ := w.store.GetHostHandoff(operationID)
	if record.State != hosthandoff.PendingRotation {
		t.Fatalf("state = %s", record.State)
	}
}

func TestImpostorAfterTheSwapGetsNothing(t *testing.T) {
	w := newWorld(t, false, true)
	if err := os.WriteFile(filepath.Join(w.dir, "bootstrap-complete"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := w.runner.Run(ctx, w.config)
	if !errors.Is(err, ErrHostKey) {
		t.Fatalf("Run = %v, want a host key mismatch", err)
	}
	if w.exists("rclone.conf") {
		t.Fatal("credentials were sent to a host that is not the pinned key")
	}
}

func TestInterruptedHandoffResumes(t *testing.T) {
	w := newWorld(t, false, false)
	// Long enough for the key swap on a slow CI machine; the run then waits
	// for a bootstrap that never finishes and times out.
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if _, err := w.runner.Run(ctx, w.config); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Run without a finished bootstrap = %v", err)
	}
	record, _ := w.store.GetHostHandoff(operationID)
	if record.State != hosthandoff.Rotated || record.CredentialsSent {
		t.Fatalf("record after the interruption = %+v", record)
	}
	if err := os.WriteFile(filepath.Join(w.dir, "bootstrap-complete"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	if report, err := w.runner.Run(ctx2, w.config); err != nil || !report.RestoreComplete {
		t.Fatalf("resumed run = %+v, %v", report, err)
	}
	rotations, _ := os.ReadFile(filepath.Join(w.dir, "rotations"))
	if strings.Count(string(rotations), "swapped") != 1 {
		t.Fatalf("rotations = %q", rotations)
	}
}

func TestRcloneConfigRefusesInjectedLines(t *testing.T) {
	for name, test := range map[string][3]string{
		"newline in key":   {"AKIA0123456789ABCDEF\nendpoint = evil", secretKey, "us-east-1"},
		"space in secret":  {accessKey, "short secret value 1234", "us-east-1"},
		"region injection": {accessKey, secretKey, "us-east-1\nendpoint"},
		"too short":        {"abc", secretKey, "us-east-1"},
	} {
		if _, err := RcloneConfig([]byte(test[0]), []byte(test[1]), test[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	conf, err := RcloneConfig([]byte(accessKey+"\n"), []byte(secretKey), "eu-central-1")
	if err != nil || strings.Count(string(conf), "\n") != 8 {
		t.Fatalf("config = %q, %v", conf, err)
	}
}

// lyingScan reports the expected key whatever the server presents, so only
// ssh's own strict host key check stands between the impostor and the swap.
type lyingScan struct {
	OpenSSH
	claim string
}

func (l lyingScan) HostKey(context.Context, Target) (string, error) { return l.claim, nil }

func TestSSHItselfRefusesAnImpostorEvenIfTheScanIsFooled(t *testing.T) {
	w := newWorld(t, true, false)
	w.runner.SSH = lyingScan{OpenSSH: w.runner.SSH.(OpenSSH), claim: w.oneTimeKey}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := w.runner.Run(ctx, w.config); !errors.Is(err, ErrHostKey) {
		t.Fatalf("Run = %v, want ssh to refuse the host key", err)
	}
	if w.exists("rotations") || w.exists("rclone.conf") {
		t.Fatal("the impostor ran the swap or received credentials")
	}
}

// slowSSH answers the host key check, then blocks every command until the
// deadline, the way a killed ssh would.
type slowSSH struct{ key string }

func (s slowSSH) HostKey(context.Context, Target) (string, error) { return s.key, nil }
func (s slowSSH) Run(ctx context.Context, _ Target, _, _ string, _ []byte) (Result, error) {
	<-ctx.Done()
	return Result{ExitCode: -1}, nil
}

func TestADeadlineMidCommandIsATimeoutNotAFailure(t *testing.T) {
	w := newWorld(t, false, false)
	if _, err := w.store.UpdateHostHandoff(operationID, func(r *store.HostHandoffRecord) error {
		r.ConnectOneTime(r.OneTimeHostKey, time.Now())
		r.Rotate(w.swapKey + "\n")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.runner.SSH = slowSSH{key: w.swapKey}
	w.provider.fail = true
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := w.runner.Run(ctx, w.config); !errors.Is(err, ErrTimedOut) {
		t.Fatalf("Run = %v, want ErrTimedOut", err)
	}
}
