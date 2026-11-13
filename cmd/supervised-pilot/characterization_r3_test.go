package main

// Characterization tests written before the r3 refactor of main.go. They pin
// the exact CLI output, error messages, audit events, confirmation rules, and
// which provider or SSH calls each path makes, so that the split into named
// stages and sibling files can be checked as behaviour-preserving.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const pilotR3OperationID = "0123456789abcdef0123456789abcdef"

func pilotR3Run(t *testing.T, statePath string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runWithSettings(args, &stdout, &stderr, runtimeSettings{stateDBPath: statePath})
	return stdout.String(), stderr.String(), err
}

func pilotR3ErrorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func pilotR3Digest() string {
	digest := sha256.Sum256([]byte("bootstrap"))
	return hex.EncodeToString(digest[:])
}

// pilotR3Vultr is a fake Vultr API behind http.DefaultTransport, which the
// production client uses. It records each call as "METHOD /path".
type pilotR3Vultr struct {
	mu        sync.Mutex
	calls     []string
	responses map[string]func(body []byte) (int, string)
}

func (fake *pilotR3Vultr) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	key := request.Method + " " + request.URL.Path
	fake.mu.Lock()
	fake.calls = append(fake.calls, key)
	respond := fake.responses[key]
	fake.mu.Unlock()
	if request.URL.Host != "api.vultr.com" || respond == nil {
		return nil, fmt.Errorf("unexpected Vultr call %s %s", request.Method, request.URL)
	}
	status, text := respond(body)
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(text)), Header: http.Header{"Content-Type": {"application/json"}}, Request: request}, nil
}

func (fake *pilotR3Vultr) recorded() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.calls...)
}

func pilotR3InstallVultr(t *testing.T, controllerKey string) *pilotR3Vultr {
	t.Helper()
	var mu sync.Mutex
	label := ""
	fake := &pilotR3Vultr{responses: map[string]func([]byte) (int, string){
		"GET /v2/startup-scripts/script-123": func([]byte) (int, string) {
			return 200, fmt.Sprintf(`{"startup_script":{"id":"script-123","name":"bootstrap","type":"boot","script":%q}}`, base64.StdEncoding.EncodeToString([]byte("bootstrap")))
		},
		"GET /v2/ssh-keys/sshkey-123": func([]byte) (int, string) {
			return 200, fmt.Sprintf(`{"ssh_key":{"id":"sshkey-123","name":"gsc","ssh_key":%q}}`, controllerKey)
		},
		"GET /v2/firewalls/firewall-123/rules": func([]byte) (int, string) {
			return 200, `{"firewall_rules":[{"id":1,"ip_type":"v4","action":"accept","protocol":"tcp","port":"22","subnet":"203.0.113.10","subnet_size":32,"source":""}],"meta":{"total":1,"links":{"next":""}}}`
		},
		"POST /v2/instances": func(body []byte) (int, string) {
			var request struct {
				Label string `json:"label"`
			}
			_ = json.Unmarshal(body, &request)
			mu.Lock()
			label = request.Label
			mu.Unlock()
			return 202, fmt.Sprintf(`{"instance":{"id":"instance-1","label":%q,"region":"ewr","plan":"vc2-1c-2gb","os_id":362}}`, request.Label)
		},
		"GET /v2/instances/instance-1": func([]byte) (int, string) {
			mu.Lock()
			defer mu.Unlock()
			return 200, fmt.Sprintf(`{"instance":{"id":"instance-1","label":%q,"region":"ewr","plan":"vc2-1c-2gb","os_id":362}}`, label)
		},
		"GET /v2/instances": func([]byte) (int, string) {
			return 200, `{"instances":[],"meta":{"total":0,"links":{"next":""}}}`
		},
	}}
	previous := http.DefaultTransport
	http.DefaultTransport = fake
	t.Cleanup(func() { http.DefaultTransport = previous })
	return fake
}

// pilotR3Credentials writes a systemd credential directory and returns the
// controller's authorized public key.
func pilotR3Credentials(t *testing.T, withToken, withSSHKey bool) string {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", directory)
	public, private, err := sshkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if withToken {
		if err := os.WriteFile(filepath.Join(directory, "vultr-pilot-api-token"), []byte("test-token"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if withSSHKey {
		encoded, err := sshkey.MarshalPrivateKey(private, "controller")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "gsc-pilot-ssh-key"), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return sshkey.AuthorizedKey(public)
}

func pilotR3Audit(t *testing.T, home string) []auditRecord {
	t.Helper()
	file, err := os.Open(filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []auditRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record auditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

// pilotR3AuditSummary renders records as "event|action|target|service|detail".
func pilotR3AuditSummary(records []auditRecord) []string {
	var lines []string
	for _, record := range records {
		lines = append(lines, strings.Join([]string{record.Event, record.Action, record.Target, record.Service, record.Detail}, "|"))
	}
	return lines
}

func pilotR3Equal(t *testing.T, name string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n got: %q\nwant: %q", name, got, want)
	}
}

func pilotR3Home(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// pilotR3BrokenAuditHome makes HOME/.local a regular file so that creating
// the audit directory fails.
func pilotR3BrokenAuditHome(t *testing.T) string {
	t.Helper()
	home := pilotR3Home(t)
	if err := os.WriteFile(filepath.Join(home, ".local"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
}

func pilotR3ExecuteArgs(extra ...string) []string {
	args := append(provisionArgs(), "--execute", "--confirm-create", "pilot-test@staging", "--operation-id", pilotR3OperationID)
	return append(args, extra...)
}

func TestPilotR3ProvisionDryRunOutputAndNoSideEffects(t *testing.T) {
	home := pilotR3Home(t)
	t.Setenv("CREDENTIALS_DIRECTORY", filepath.Join(t.TempDir(), "missing"))
	fake := pilotR3InstallVultr(t, "")
	statePath := filepath.Join(t.TempDir(), "state", "pilot.db")
	stdout, stderr, err := pilotR3Run(t, statePath, provisionArgs()...)
	if err != nil {
		t.Fatal(err)
	}
	want := "Dry run: no provider API call, credential read, or capacity reservation was made.\n" +
		"Would create one managed Vultr VM: server=pilot-test environment=staging group=shared_nonproduction region=ewr plan=vc2-1c-2gb os_id=362\n" +
		"Bootstrap startup script ID: script-123\nBootstrap SHA-256: " + pilotR3Digest() + "\n" +
		"Firewall group firewall-123 must admit SSH only from 203.0.113.10; this is checked before the create.\n" +
		"A single-use SSH host key will be generated for this VM; only its public half is stored.\n" +
		"Configured hard limit: 2 controller-owned VM(s).\n" +
		"To create it, add --execute --confirm-create pilot-test@staging.\n"
	if stdout != want || stderr != "" {
		t.Fatalf("dry run stdout=%q stderr=%q", stdout, stderr)
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("dry run called Vultr: %v", calls)
	}
	if records := pilotR3Audit(t, home); len(records) != 0 {
		t.Fatalf("dry run wrote audit records: %v", records)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("dry run created state: %v", err)
	}
}

func TestPilotR3ProvisionValidationMessages(t *testing.T) {
	digest := pilotR3Digest()
	replace := func(name, value string) []string {
		return append(withoutFlags(provisionArgs(), name), name, value)
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"zero timeout", append(provisionArgs(), "--timeout", "0s"), "--timeout must be greater than zero and no more than 24h"},
		{"long timeout", append(provisionArgs(), "--timeout", "25h"), "--timeout must be greater than zero and no more than 24h"},
		{"timeout before server", append(replace("--server-id", "Bad"), "--timeout", "0s"), "--timeout must be greater than zero and no more than 24h"},
		{"server id", replace("--server-id", "Bad"), "--server-id must be a lower-case identifier"},
		{"missing server id", withoutFlags(provisionArgs(), "--server-id"), "--server-id must be a lower-case identifier"},
		{"no ssh key", withoutFlags(provisionArgs(), "--ssh-key-id"), "at least one --ssh-key-id is required so the VM is reachable"},
		{"no script id", withoutFlags(provisionArgs(), "--startup-script-id"), "--startup-script-id and a 64-character lowercase --startup-script-sha256 are required"},
		{"upper digest", replace("--startup-script-sha256", strings.ToUpper(digest)), "--startup-script-id and a 64-character lowercase --startup-script-sha256 are required"},
		{"credential ref", append(provisionArgs(), "--credential-ref", "file:///token"), "--credential-ref and --ssh-credential-ref must use systemd-credential://NAME"},
		{"ssh credential ref", append(provisionArgs(), "--ssh-credential-ref", "bad"), "--credential-ref and --ssh-credential-ref must use systemd-credential://NAME"},
		{"no firewall", withoutFlags(provisionArgs(), "--firewall-group-id"), "--firewall-group-id and an exact --controller-ip address (not a range) are required so SSH is reachable only from the controller"},
		{"ip range", replace("--controller-ip", "203.0.113.0/24"), "--firewall-group-id and an exact --controller-ip address (not a range) are required so SSH is reachable only from the controller"},
		{"environment", replace("--environment", "qa"), "unsupported pilot capacity environment"},
		{"os id", replace("--os-id", "abc"), "--os-id must be a positive Vultr OS ID"},
		{"zero os id", replace("--os-id", "0"), "--os-id must be a positive Vultr OS ID"},
		{"region", withoutFlags(provisionArgs(), "--region"), "region, plan, hostname, SSH key ID, or firewall group ID is invalid"},
		{"hostname", replace("--hostname", "bad host"), "region, plan, hostname, SSH key ID, or firewall group ID is invalid"},
		{"confirm without execute", append(provisionArgs(), "--confirm-create", "pilot-test@staging"), "--confirm-create requires --execute"},
		{"operation without execute", append(provisionArgs(), "--operation-id", pilotR3OperationID), "--operation-id requires --execute"},
		{"confirm checked before operation", append(provisionArgs(), "--confirm-create", "x", "--operation-id", "x"), "--confirm-create requires --execute"},
		{"missing confirm", append(provisionArgs(), "--execute"), `Vultr create not started: --confirm-create must exactly match "pilot-test@staging"`},
		{"wrong confirm", append(provisionArgs(), "--execute", "--confirm-create", "pilot-test@testing"), `Vultr create not started: --confirm-create must exactly match "pilot-test@staging"`},
		{"bad operation id", append(provisionArgs(), "--execute", "--confirm-create", "pilot-test@staging", "--operation-id", "ABC"), "--operation-id must be 32 lowercase hexadecimal characters"},
		{"unsupported flag", append(provisionArgs(), "--host", "example"), "remove flags not supported by this action: --host"},
		{"two unsupported flags", append(provisionArgs(), "--host", "example", "--instance-id", "x"), "remove flags not supported by this action: --host, --instance-id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := pilotR3Home(t)
			t.Setenv("CREDENTIALS_DIRECTORY", "")
			fake := pilotR3InstallVultr(t, "")
			statePath := filepath.Join(t.TempDir(), "state", "pilot.db")
			stdout, stderr, err := pilotR3Run(t, statePath, test.args...)
			if pilotR3ErrorText(err) != test.want {
				t.Fatalf("error = %q, want %q", pilotR3ErrorText(err), test.want)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("rejected provision printed stdout=%q stderr=%q", stdout, stderr)
			}
			if calls := fake.recorded(); len(calls) != 0 {
				t.Fatalf("rejected provision called Vultr: %v", calls)
			}
			if records := pilotR3Audit(t, home); len(records) != 0 {
				t.Fatalf("rejected provision wrote audit records: %v", records)
			}
			if _, err := os.Stat(statePath); !os.IsNotExist(err) {
				t.Fatalf("rejected provision created state: %v", err)
			}
		})
	}
}

func TestPilotR3ProvisionExecuteAuditPathFailures(t *testing.T) {
	t.Run("no home", func(t *testing.T) {
		t.Setenv("HOME", "")
		_, _, err := pilotR3Run(t, filepath.Join(t.TempDir(), "pilot.db"), pilotR3ExecuteArgs()...)
		if pilotR3ErrorText(err) != "resolve audit log path: $HOME is not defined" {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("audit not writable", func(t *testing.T) {
		auditPath := pilotR3BrokenAuditHome(t)
		fake := pilotR3InstallVultr(t, "")
		_, _, err := pilotR3Run(t, filepath.Join(t.TempDir(), "pilot.db"), pilotR3ExecuteArgs()...)
		want := "Vultr create not started: could not persist the audit event: mkdir " + filepath.Dir(filepath.Dir(filepath.Dir(auditPath))) + ": not a directory"
		if pilotR3ErrorText(err) != want {
			t.Fatalf("error = %q, want %q", pilotR3ErrorText(err), want)
		}
		if calls := fake.recorded(); len(calls) != 0 {
			t.Fatalf("calls = %v", calls)
		}
	})
}

func TestPilotR3ProvisionExecuteStagesBeforeProvider(t *testing.T) {
	requested := "provision_requested|provision|pilot-test@staging|vultr.instance.create|region=ewr plan=vc2-1c-2gb os_id=362 startup_script_id=script-123 startup_script_sha256=" + pilotR3Digest()
	t.Run("credential directory missing", func(t *testing.T) {
		home := pilotR3Home(t)
		t.Setenv("CREDENTIALS_DIRECTORY", "")
		fake := pilotR3InstallVultr(t, "")
		statePath := filepath.Join(t.TempDir(), "pilot.db")
		stdout, stderr, err := pilotR3Run(t, statePath, pilotR3ExecuteArgs()...)
		if pilotR3ErrorText(err) != "load systemd credential directory: systemd credential directory is required" || stdout != "" || stderr != "" {
			t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
		}
		pilotR3Equal(t, "audit", pilotR3AuditSummary(pilotR3Audit(t, home)), []string{requested,
			"provision_failed|provision|pilot-test@staging|vultr.instance.create|systemd credential directory unavailable"})
		if len(fake.recorded()) != 0 {
			t.Fatal("called Vultr")
		}
		if _, err := os.Stat(statePath); !os.IsNotExist(err) {
			t.Fatal("created state")
		}
	})
	t.Run("controller key missing", func(t *testing.T) {
		home := pilotR3Home(t)
		pilotR3Credentials(t, true, false)
		fake := pilotR3InstallVultr(t, "")
		statePath := filepath.Join(t.TempDir(), "pilot.db")
		stdout, stderr, err := pilotR3Run(t, statePath, pilotR3ExecuteArgs()...)
		if pilotR3ErrorText(err) != "the controller SSH key credential is missing or is not an unencrypted ed25519 key; create it with controller-setup ssh-key generate" || stdout != "" || stderr != "" {
			t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
		}
		pilotR3Equal(t, "audit", pilotR3AuditSummary(pilotR3Audit(t, home)), []string{requested,
			"provision_failed|provision|pilot-test@staging|vultr.instance.create|controller SSH key credential unavailable"})
		if len(fake.recorded()) != 0 {
			t.Fatal("called Vultr")
		}
		if _, err := os.Stat(statePath); !os.IsNotExist(err) {
			t.Fatal("created state")
		}
	})
	t.Run("capacity store unavailable", func(t *testing.T) {
		home := pilotR3Home(t)
		pilotR3Credentials(t, true, true)
		fake := pilotR3InstallVultr(t, "")
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, err := pilotR3Run(t, filepath.Join(blocker, "pilot.db"), pilotR3ExecuteArgs()...)
		if !strings.HasPrefix(pilotR3ErrorText(err), "open capacity store: ") || strings.Contains(pilotR3ErrorText(err), "audit") || stdout != "" || stderr != "" {
			t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
		}
		pilotR3Equal(t, "audit", pilotR3AuditSummary(pilotR3Audit(t, home)), []string{requested,
			"provision_failed|provision|pilot-test@staging|vultr.instance.create|capacity store unavailable"})
		if len(fake.recorded()) != 0 {
			t.Fatal("called Vultr")
		}
	})
}

func pilotR3Submitted(operationID string) string {
	return "Operation ID: " + operationID + "\n" +
		"Submitting capacity-checked Vultr create for pilot-test in staging.\n" +
		"This creates a billable VM; game installation and readiness are not part of this command.\n"
}

func TestPilotR3ProvisionExecuteProviderOutcomes(t *testing.T) {
	requested := "provision_requested|provision|pilot-test@staging|vultr.instance.create|region=ewr plan=vc2-1c-2gb os_id=362 startup_script_id=script-123 startup_script_sha256=" + pilotR3Digest()
	checks := []string{"GET /v2/startup-scripts/script-123", "GET /v2/ssh-keys/sshkey-123", "GET /v2/firewalls/firewall-123/rules"}
	tests := []struct {
		name       string
		withToken  bool
		occupied   bool
		configure  func(*pilotR3Vultr)
		wantErr    string
		wantStdout string
		wantStderr string
		wantAudit  string
		wantCalls  []string
	}{
		{
			name: "api token missing", withToken: false,
			wantErr: "systemd credential is unavailable", wantStdout: pilotR3Submitted(pilotR3OperationID),
			wantAudit: "provision_failed|provision|pilot-test@staging|vultr.instance.create|create request did not complete",
		},
		{
			name: "bootstrap script mismatch", withToken: true,
			configure: func(fake *pilotR3Vultr) {
				fake.responses["GET /v2/startup-scripts/script-123"] = func([]byte) (int, string) { return 404, `{"error":"not found"}` }
			},
			wantErr: "Vultr startup script ID or digest does not match the approved bootstrap", wantStdout: pilotR3Submitted(pilotR3OperationID),
			wantAudit: "provision_rejected|provision|pilot-test@staging|vultr.instance.create|pre-create check failed; nothing was sent to Vultr",
			wantCalls: checks[:1],
		},
		{
			name: "capacity occupied", withToken: true, occupied: true,
			wantErr: "managed VM capacity reached", wantStdout: pilotR3Submitted(pilotR3OperationID),
			wantStderr: "Provision request rejected before provider create. Operation ID: " + pilotR3OperationID + "\n",
			wantAudit:  "provision_rejected|provision|pilot-test@staging|vultr.instance.create|capacity policy rejected the request before provider dispatch",
			wantCalls:  checks,
		},
		{
			name: "provider rejected", withToken: true,
			configure: func(fake *pilotR3Vultr) {
				fake.responses["POST /v2/instances"] = func([]byte) (int, string) { return 422, `{"error":"bad plan"}` }
			},
			wantErr: "Vultr rejected the pilot create request", wantStdout: pilotR3Submitted(pilotR3OperationID),
			wantAudit: "provider_rejected|provision|pilot-test@staging|vultr.instance.create|provider definitively rejected create; reservation was released",
			wantCalls: append(append([]string(nil), checks...), "POST /v2/instances"),
		},
		{
			name: "outcome unknown", withToken: true,
			configure: func(fake *pilotR3Vultr) {
				fake.responses["POST /v2/instances"] = func([]byte) (int, string) { return 500, `{}` }
			},
			wantErr: "pilot Vultr create outcome is unknown", wantStdout: pilotR3Submitted(pilotR3OperationID),
			wantStderr: "The slot remains occupied. Re-run with --operation-id " + pilotR3OperationID + " and the same create parameters to reconcile; the service will not resend an uncertain create.\n",
			wantAudit:  "outcome_unknown|provision|pilot-test@staging|vultr.instance.create|provider result requires reconciliation; capacity remains occupied",
			wantCalls:  append(append([]string(nil), checks...), "POST /v2/instances", "GET /v2/instances"),
		},
		{
			name: "confirmed", withToken: true,
			wantStdout: pilotR3Submitted(pilotR3OperationID) +
				"Vultr instance instance-1 is confirmed present and counted (capacity state active).\n" +
				"Provider presence is confirmed; game readiness is not verified.\n" +
				"Next: gsc-pilot handoff --operation-id " + pilotR3OperationID + " --wasabi-region <region> verifies the VM's host key, then sends the Wasabi restore credentials.\n",
			wantAudit: "provision_confirmed|provision|pilot-test@staging|vultr.instance.create|exact Vultr instance ID and managed label were observed",
			wantCalls: append(append([]string(nil), checks...), "POST /v2/instances", "GET /v2/instances/instance-1"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := pilotR3Home(t)
			controllerKey := pilotR3Credentials(t, test.withToken, true)
			fake := pilotR3InstallVultr(t, controllerKey)
			if test.configure != nil {
				test.configure(fake)
			}
			statePath := filepath.Join(t.TempDir(), "pilot.db")
			if test.occupied {
				repository, err := store.OpenBolt(statePath)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := repository.ReserveCapacity(store.CapacityReservation{
					OperationID: "fedcba9876543210fedcba9876543210", ServerID: "other", EnvironmentID: "testing",
					CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-managed-other",
				}, 2, 1); err != nil {
					t.Fatal(err)
				}
				if err := repository.Close(); err != nil {
					t.Fatal(err)
				}
			}
			stdout, stderr, err := pilotR3Run(t, statePath, pilotR3ExecuteArgs()...)
			wantErr := test.wantErr
			if wantErr == "" {
				wantErr = "<nil>"
			}
			if pilotR3ErrorText(err) != wantErr {
				t.Fatalf("error = %q, want %q", pilotR3ErrorText(err), wantErr)
			}
			if stdout != test.wantStdout || stderr != test.wantStderr {
				t.Fatalf("stdout=%q\nwant  %q\nstderr=%q\nwant  %q", stdout, test.wantStdout, stderr, test.wantStderr)
			}
			pilotR3Equal(t, "audit", pilotR3AuditSummary(pilotR3Audit(t, home)), []string{requested, test.wantAudit})
			pilotR3Equal(t, "Vultr calls", fake.recorded(), test.wantCalls)
		})
	}
}

func TestPilotR3ProvisionGeneratesOperationID(t *testing.T) {
	home := pilotR3Home(t)
	controllerKey := pilotR3Credentials(t, true, true)
	pilotR3InstallVultr(t, controllerKey)
	args := append(provisionArgs(), "--execute", "--confirm-create", "pilot-test@staging")
	stdout, _, err := pilotR3Run(t, filepath.Join(t.TempDir(), "pilot.db"), args...)
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`^Operation ID: ([0-9a-f]{32})\n`).FindStringSubmatch(stdout)
	if match == nil {
		t.Fatalf("stdout = %q", stdout)
	}
	records := pilotR3Audit(t, home)
	if len(records) != 2 || records[0].OperationID != match[1] || records[1].OperationID != match[1] {
		t.Fatalf("audit = %v", records)
	}
}

// pilotR3FakeSSH installs an ssh that answers the Nth "systemctl show" with
// shows[N-1] (or exits 5 printing it after "FAIL:") and the start command by
// exiting startExit after printing startOutput to stderr.
func pilotR3FakeSSH(t *testing.T, shows []string, startExit int, startOutput string) string {
	t.Helper()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	countPath := logPath + ".count"
	var script strings.Builder
	script.WriteString("#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + pilotR3ShellQuote(logPath) + "\n")
	script.WriteString("case \"$*\" in\n  *\"systemctl show\"*)\n    n=0\n")
	script.WriteString("    if [ -f " + pilotR3ShellQuote(countPath) + " ]; then n=$(cat " + pilotR3ShellQuote(countPath) + "); fi\n")
	script.WriteString("    n=$((n + 1))\n    printf '%s' \"$n\" > " + pilotR3ShellQuote(countPath) + "\n    case \"$n\" in\n")
	for i, show := range shows {
		if rest, failed := strings.CutPrefix(show, "FAIL:"); failed {
			fmt.Fprintf(&script, "      %d) printf '%%s\\n' %s >&2; exit 5 ;;\n", i+1, pilotR3ShellQuote(rest))
		} else {
			fmt.Fprintf(&script, "      %d) printf '%%s\\n' %s ;;\n", i+1, pilotR3ShellQuote(show))
		}
	}
	script.WriteString("      *) echo 'unexpected show' >&2; exit 65 ;;\n    esac ;;\n")
	fmt.Fprintf(&script, "  *\"systemctl start --wait minecraft-backup.service\"*)\n    printf '%%s' %s >&2\n    exit %d ;;\n", pilotR3ShellQuote(startOutput), startExit)
	script.WriteString("  *) echo 'unexpected SSH command' >&2; exit 64 ;;\nesac\n")
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func pilotR3ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func pilotR3Unit(active, sub, result, exit, invocation string) string {
	return fmt.Sprintf("ActiveState=%s\nSubState=%s\nResult=%s\nExecMainStatus=%s\nInvocationID=%s", active, sub, result, exit, invocation)
}

func pilotR3SSHCalls(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

const (
	pilotR3InvocationA = "00000000000000000000000000000001"
	pilotR3InvocationB = "00000000000000000000000000000002"
	pilotR3ShowCall    = "-T -o BatchMode=yes -o ConnectTimeout=10 server.example sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus,InvocationID minecraft-backup.service"
	pilotR3StartCall   = "-T -o BatchMode=yes -o ConnectTimeout=10 server.example sudo -n systemctl start --wait minecraft-backup.service"
)

func TestPilotR3BackupDryRunAndConfirmation(t *testing.T) {
	logPath := pilotR3FakeSSH(t, nil, 0, "")
	home := pilotR3Home(t)
	stdout, stderr, err := pilotR3Run(t, "", "backup", "--host", "server.example")
	want := "Dry run: would check minecraft-backup.service on server.example, start it, and inspect the resulting systemd invocation.\n" +
		"The Paper service remains online; this uses the existing backup service.\n" +
		"To run it, add --execute --confirm-host server.example\n"
	if err != nil || stdout != want || stderr != "" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	for _, args := range [][]string{
		{"backup", "--host", "server.example", "--execute"},
		{"backup", "--host", "server.example", "--execute", "--confirm-host", "other.example"},
	} {
		_, _, err := pilotR3Run(t, "", args...)
		if pilotR3ErrorText(err) != "backup not started: --confirm-host must exactly match --host" {
			t.Fatalf("%v: %v", args, err)
		}
	}
	// A dry run ignores --confirm-host.
	if _, _, err := pilotR3Run(t, "", "backup", "--host", "server.example", "--confirm-host", "other"); err != nil {
		t.Fatal(err)
	}
	if calls := pilotR3SSHCalls(t, logPath); calls != nil {
		t.Fatalf("SSH calls = %v", calls)
	}
	if records := pilotR3Audit(t, home); records != nil {
		t.Fatalf("audit = %v", records)
	}
	t.Setenv("HOME", "")
	_, _, err = pilotR3Run(t, "", "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
	if pilotR3ErrorText(err) != "resolve audit log path: $HOME is not defined" {
		t.Fatalf("err = %v", err)
	}
	if calls := pilotR3SSHCalls(t, logPath); calls != nil {
		t.Fatalf("SSH calls = %v", calls)
	}
}

func TestPilotR3BackupOutcomes(t *testing.T) {
	idle := pilotR3Unit("inactive", "dead", "success", "0", pilotR3InvocationA)
	done := pilotR3Unit("inactive", "dead", "success", "0", pilotR3InvocationB)
	tests := []struct {
		name        string
		shows       []string
		startExit   int
		startOutput string
		wantErr     string // %OP% and %AUDIT% are replaced
		wantStdout  string
		wantStderr  string
		wantAudit   []string
		wantCalls   []string
	}{
		{
			name: "preflight fails", shows: []string{"FAIL:boom"},
			wantErr:   "operation %OP% rejected (audit log: %AUDIT%): preflight minecraft-backup.service on server.example: exit status 5: boom",
			wantAudit: []string{"rejected|backup|server.example|minecraft-backup.service|preflight_failed"},
			wantCalls: []string{pilotR3ShowCall},
		},
		{
			name: "preflight unexpected response", shows: []string{"Nothing=here"},
			wantErr:   "operation %OP% rejected (audit log: %AUDIT%): preflight minecraft-backup.service on server.example: unexpected systemctl response for minecraft-backup.service",
			wantAudit: []string{"rejected|backup|server.example|minecraft-backup.service|preflight_failed"},
			wantCalls: []string{pilotR3ShowCall},
		},
		{
			name: "unit failed", shows: []string{pilotR3Unit("failed", "failed", "exit-code", "1", pilotR3InvocationA)},
			wantErr:   "operation %OP% rejected (audit log: %AUDIT%): refusing to retry failed unit minecraft-backup.service; inspect its journal and clear the failure manually",
			wantAudit: []string{"rejected|backup|server.example|minecraft-backup.service|unit_failed"},
			wantCalls: []string{pilotR3ShowCall},
		},
		{
			name: "previous result failed", shows: []string{pilotR3Unit("inactive", "dead", "failed", "1", pilotR3InvocationA)},
			wantErr:   "operation %OP% rejected (audit log: %AUDIT%): refusing to retry failed unit minecraft-backup.service; inspect its journal and clear the failure manually",
			wantAudit: []string{"rejected|backup|server.example|minecraft-backup.service|unit_failed"},
			wantCalls: []string{pilotR3ShowCall},
		},
		{
			name: "unit active", shows: []string{pilotR3Unit("active", "running", "success", "0", pilotR3InvocationA)},
			wantErr:   `operation %OP% rejected (audit log: %AUDIT%): refusing to start minecraft-backup.service while its state is "active"; inspect the unit manually first`,
			wantAudit: []string{"rejected|backup|server.example|minecraft-backup.service|unit_not_inactive"},
			wantCalls: []string{pilotR3ShowCall},
		},
		{
			name: "start fails", shows: []string{idle}, startExit: 7, startOutput: "remote said no",
			wantErr:    "backup outcome unknown; inspect minecraft-backup.service on server.example: exit status 7",
			wantStderr: "remote said no\n",
			wantAudit: []string{
				`outcome_unknown|backup|server.example|minecraft-backup.service|systemctl start returned an error; remote outcome may be unknown (previous invocation_id="` + pilotR3InvocationA + `")`,
			},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall},
		},
		{
			name: "start fails silently", shows: []string{idle}, startExit: 7,
			wantErr: "backup outcome unknown; inspect minecraft-backup.service on server.example: exit status 7",
			wantAudit: []string{
				`outcome_unknown|backup|server.example|minecraft-backup.service|systemctl start returned an error; remote outcome may be unknown (previous invocation_id="` + pilotR3InvocationA + `")`,
			},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall},
		},
		{
			name: "post status unreadable", shows: []string{idle, "FAIL:gone"},
			wantErr:   "backup outcome unknown; verify minecraft-backup.service on server.example: exit status 5: gone",
			wantAudit: []string{"outcome_unknown|backup|server.example|minecraft-backup.service|start returned successfully, but post-run status could not be read"},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall, pilotR3ShowCall},
		},
		{
			name: "invocation unchanged", shows: []string{idle, idle},
			wantErr:   `backup outcome unknown; systemd invocation ID is missing, malformed, or unchanged: before="` + pilotR3InvocationA + `" after="` + pilotR3InvocationA + `"; inspect minecraft-backup.service on server.example before retrying`,
			wantAudit: []string{`outcome_unknown|backup|server.example|minecraft-backup.service|systemd invocation ID is missing, malformed, or unchanged: before="` + pilotR3InvocationA + `" after="` + pilotR3InvocationA + `"`},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall, pilotR3ShowCall},
		},
		{
			name: "previous invocation malformed", shows: []string{pilotR3Unit("inactive", "dead", "success", "0", "XYZ"), done},
			wantErr:   `backup outcome unknown; systemd invocation ID is missing, malformed, or unchanged: before="XYZ" after="` + pilotR3InvocationB + `"; inspect minecraft-backup.service on server.example before retrying`,
			wantAudit: []string{`outcome_unknown|backup|server.example|minecraft-backup.service|systemd invocation ID is missing, malformed, or unchanged: before="XYZ" after="` + pilotR3InvocationB + `"`},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall, pilotR3ShowCall},
		},
		{
			name: "not finished", shows: []string{idle, pilotR3Unit("activating", "start", "success", "0", pilotR3InvocationB)},
			wantErr:   `backup outcome unknown; systemd start job returned before the unit reached a completed state: invocation_id="` + pilotR3InvocationB + `" active="activating" sub="start"; inspect minecraft-backup.service on server.example before retrying`,
			wantAudit: []string{`outcome_unknown|backup|server.example|minecraft-backup.service|systemd start job returned before the unit reached a completed state: invocation_id="` + pilotR3InvocationB + `" active="activating" sub="start"`},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall, pilotR3ShowCall},
		},
		{
			name: "service failed", shows: []string{idle, pilotR3Unit("inactive", "dead", "exit-code", "2", pilotR3InvocationB)},
			wantErr:   `minecraft-backup.service did not report success (systemd invocation_id="` + pilotR3InvocationB + `" result="exit-code" exit="2" active="inactive")`,
			wantAudit: []string{`failed|backup|server.example|minecraft-backup.service|systemd invocation_id="` + pilotR3InvocationB + `" result="exit-code" exit="2" active="inactive"`},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall, pilotR3ShowCall},
		},
		{
			name: "succeeded from a fresh unit", shows: []string{pilotR3Unit("inactive", "dead", "", "", ""), pilotR3Unit("active", "exited", "success", "0", pilotR3InvocationB)},
			wantStdout: "Starting minecraft-backup.service on server.example (timeout 1h0m0s).\n" +
				"Operation ID: %OP%\nAudit log: %AUDIT%\n" +
				"No stop, restore, or destroy command will be sent.\n" +
				"Backup service completed successfully on server.example (operation %OP%).\n" +
				"This confirms the systemd unit result only; it does not verify backup contents or storage integrity.\n" +
				"minecraft-backup.service: active=active sub=exited result=success exit=0 invocation=" + pilotR3InvocationB + "\n",
			wantAudit: []string{"service_succeeded|backup|server.example|minecraft-backup.service|systemd invocation_id=" + pilotR3InvocationB + " reported result=success and exit status 0; backup contents and storage integrity are not verified"},
			wantCalls: []string{pilotR3ShowCall, pilotR3StartCall, pilotR3ShowCall},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := pilotR3Home(t)
			auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
			logPath := pilotR3FakeSSH(t, test.shows, test.startExit, test.startOutput)
			stdout, stderr, err := pilotR3Run(t, "", "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
			records := pilotR3Audit(t, home)
			if len(records) == 0 {
				t.Fatal("no audit records")
			}
			operationID := records[0].OperationID
			expand := func(value string) string {
				return strings.NewReplacer("%OP%", operationID, "%AUDIT%", auditPath).Replace(value)
			}
			wantErr := expand(test.wantErr)
			if wantErr == "" {
				wantErr = "<nil>"
			}
			if pilotR3ErrorText(err) != wantErr {
				t.Fatalf("error = %q\n want %q", pilotR3ErrorText(err), wantErr)
			}
			wantStdout := expand(test.wantStdout)
			if test.wantStdout == "" && len(test.shows) > 0 && !strings.HasPrefix(test.wantErr, "operation") {
				wantStdout = "Starting minecraft-backup.service on server.example (timeout 1h0m0s).\n" +
					"Operation ID: " + operationID + "\nAudit log: " + auditPath + "\n" +
					"No stop, restore, or destroy command will be sent.\n"
			}
			if stdout != wantStdout || stderr != test.wantStderr {
				t.Fatalf("stdout=%q\nwant  %q\nstderr=%q\nwant  %q", stdout, wantStdout, stderr, test.wantStderr)
			}
			wantAudit := test.wantAudit
			if !strings.HasPrefix(test.wantErr, "operation") {
				before := records[0].Detail
				wantAudit = append([]string{"started|backup|server.example|minecraft-backup.service|" + before}, wantAudit...)
				if !strings.HasPrefix(before, "previous systemd invocation_id=") {
					t.Fatalf("started detail = %q", before)
				}
			}
			pilotR3Equal(t, "audit", pilotR3AuditSummary(records), wantAudit)
			for _, record := range records {
				if record.OperationID != operationID {
					t.Fatalf("operation IDs differ: %v", records)
				}
			}
			pilotR3Equal(t, "ssh calls", pilotR3SSHCalls(t, logPath), test.wantCalls)
		})
	}
}

func TestPilotR3BackupStartedDetailQuotesPreviousInvocation(t *testing.T) {
	home := pilotR3Home(t)
	pilotR3FakeSSH(t, []string{pilotR3Unit("inactive", "dead", "success", "0", pilotR3InvocationA), pilotR3Unit("inactive", "dead", "success", "0", pilotR3InvocationB)}, 0, "")
	if _, _, err := pilotR3Run(t, "", "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example", "--timeout", "90s"); err != nil {
		t.Fatal(err)
	}
	records := pilotR3Audit(t, home)
	if len(records) != 2 || records[0].Detail != `previous systemd invocation_id="`+pilotR3InvocationA+`"` {
		t.Fatalf("records = %v", records)
	}
}

func TestPilotR3BackupAuditFailures(t *testing.T) {
	t.Run("rejection not recorded", func(t *testing.T) {
		auditPath := pilotR3BrokenAuditHome(t)
		pilotR3FakeSSH(t, []string{pilotR3Unit("active", "running", "success", "0", pilotR3InvocationA)}, 0, "")
		_, _, err := pilotR3Run(t, "", "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
		pattern := regexp.MustCompile(`^operation [0-9a-f]{32} rejected: refusing to start minecraft-backup.service while its state is "active"; inspect the unit manually first; could not record rejection in ` +
			regexp.QuoteMeta(auditPath) + `: mkdir ` + regexp.QuoteMeta(filepath.Dir(filepath.Dir(filepath.Dir(auditPath)))) + `: not a directory$`)
		if !pattern.MatchString(pilotR3ErrorText(err)) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("start not recorded", func(t *testing.T) {
		auditPath := pilotR3BrokenAuditHome(t)
		logPath := pilotR3FakeSSH(t, []string{pilotR3Unit("inactive", "dead", "success", "0", pilotR3InvocationA)}, 0, "")
		stdout, _, err := pilotR3Run(t, "", "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
		pattern := regexp.MustCompile(`^backup not started \(operation [0-9a-f]{32}\): could not persist the audit record at ` +
			regexp.QuoteMeta(auditPath) + `: mkdir ` + regexp.QuoteMeta(filepath.Dir(filepath.Dir(filepath.Dir(auditPath)))) + `: not a directory$`)
		if !pattern.MatchString(pilotR3ErrorText(err)) || stdout != "" {
			t.Fatalf("err = %v stdout=%q", err, stdout)
		}
		pilotR3Equal(t, "ssh calls", pilotR3SSHCalls(t, logPath), []string{pilotR3ShowCall})
	})
}

func TestPilotR3StatusOutputAndErrors(t *testing.T) {
	logPath := pilotR3FakeSSH(t, []string{
		pilotR3Unit("active", "running", "success", "0", pilotR3InvocationA),
		pilotR3Unit("inactive", "dead", "success", "0", pilotR3InvocationB),
		"FAIL:timer gone",
	}, 0, "")
	stdout, stderr, err := pilotR3Run(t, "", "status", "--host", "server.example")
	if pilotR3ErrorText(err) != "read minecraft-backup.timer on server.example: exit status 5: timer gone" || stderr != "" {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
	want := "minecraft-vanilla.service: active=active sub=running result=success exit=0 invocation=" + pilotR3InvocationA + "\n" +
		"minecraft-backup.service: active=inactive sub=dead result=success exit=0 invocation=" + pilotR3InvocationB + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q", stdout)
	}
	prefix := "-T -o BatchMode=yes -o ConnectTimeout=10 server.example sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus,InvocationID "
	pilotR3Equal(t, "ssh calls", pilotR3SSHCalls(t, logPath), []string{prefix + "minecraft-vanilla.service", prefix + "minecraft-backup.service", prefix + "minecraft-backup.timer"})
}

func TestPilotR3DispatchMessages(t *testing.T) {
	home := pilotR3Home(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown action", []string{"destroy"}, `unknown action "destroy"; expected status, backup, history, boot-status, provision, adopt, handoff, register-ssh-key, or capacity`},
		{"unexpected argument", []string{"capacity", "extra"}, "unexpected arguments: extra"},
		{"unexpected arguments", []string{"status", "--host", "h", "a", "b"}, "unexpected arguments: a b"},
		{"undefined flag", []string{"status", "--bogus"}, "flag provided but not defined: -bogus"},
		{"history flags", []string{"history", "--execute"}, "history is local and read-only; remove all flags"},
		{"history false flag", []string{"history", "--execute=false"}, "history is local and read-only; remove all flags"},
		{"status flags", []string{"status", "--host", "h", "--timeout", "1m"}, "remove flags not supported by this action: --timeout"},
		{"status no host", []string{"status"}, "--host is required and must be a simple SSH alias or user@host"},
		{"status dash host", []string{"status", "--host=-oProxy"}, "--host is required and must be a simple SSH alias or user@host"},
		{"status execute", []string{"status", "--host", "h", "--execute"}, "status is read-only; remove --execute and --confirm-host"},
		{"status confirm", []string{"status", "--host", "h", "--confirm-host", "h"}, "status is read-only; remove --execute and --confirm-host"},
		{"backup flags", []string{"backup", "--host", "h", "--server-id", "x"}, "remove flags not supported by this action: --server-id"},
		{"backup host", []string{"backup", "--host", "bad host"}, "--host is required and must be a simple SSH alias or user@host"},
		{"backup zero timeout", []string{"backup", "--host", "h", "--timeout", "0s"}, "--timeout must be greater than zero and no more than 24 hours"},
		{"backup long timeout", []string{"backup", "--host", "h", "--timeout", "25h"}, "--timeout must be greater than zero and no more than 24 hours"},
		{"capacity flags", []string{"capacity", "--execute"}, "capacity reads the fixed local policy and state database; remove all flags"},
		{"boot-status flags", []string{"boot-status", "--host", "h"}, "remove flags not supported by this action: --host"},
		{"boot-status server", []string{"boot-status", "--environment", "staging"}, "--server-id is required and must be a lower-case identifier"},
		{"boot-status environment", []string{"boot-status", "--server-id", "pilot-test", "--environment", "qa"}, "--environment must be production, staging, or testing"},
		{"adopt flags", []string{"adopt", "--host", "h"}, "remove flags not supported by this action: --host"},
		{"adopt os id", []string{"adopt", "--os-id", "abc"}, "invalid Vultr API request: provider OS ID must be a positive integer"},
		{"handoff flags", []string{"handoff", "--host", "h"}, "remove flags not supported by this action: --host"},
		{"handoff timeout", []string{"handoff", "--operation-id", pilotR3OperationID, "--wasabi-region", "us-east-1", "--timeout", "7h"}, "--timeout must be greater than zero and no more than 6h"},
		{"register flags", []string{"register-ssh-key", "--host", "h"}, "remove flags not supported by this action: --host"},
		{"register relative", []string{"register-ssh-key", "--public-key-file", "key.pub"}, "--public-key-file must be an absolute path to the key printed by controller-setup ssh-key generate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := pilotR3Run(t, filepath.Join(t.TempDir(), "pilot.db"), test.args...)
			if pilotR3ErrorText(err) != test.want {
				t.Fatalf("error = %q, want %q", pilotR3ErrorText(err), test.want)
			}
		})
	}
	if records := pilotR3Audit(t, home); records != nil {
		t.Fatalf("audit = %v", records)
	}
}

func TestPilotR3UsageAndHelp(t *testing.T) {
	stdout, stderr, err := pilotR3Run(t, "")
	if pilotR3ErrorText(err) != "an action is required" || stdout != "" || !strings.HasPrefix(stderr, "Supervised Minecraft server pilot\n  gsc-pilot status --host <ssh-alias>\n") {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	stdout, stderr, err = pilotR3Run(t, "", "status", "-h")
	if err != nil || stdout != "" || !strings.HasPrefix(stderr, "Usage of gsc-pilot status:\n") {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	for _, line := range []string{
		"  -confirm-create string\n    \trepeat <server-id>@<environment> to approve billable Vultr VM creation\n",
		"  -credential-ref string\n    \tsystemd credential reference for the Vultr API key (default \"systemd-credential://vultr-pilot-api-token\")\n",
		"  -ssh-key-id value\n    \tVultr SSH key ID; repeat for multiple keys (required for provisioning)\n",
		"  -timeout duration\n    \tmaximum time to wait for the selected operation (default 1h0m0s)\n",
		"  -execute\n    \texecute the selected action; mutating actions are dry runs by default\n",
	} {
		if !strings.Contains(stderr, line) {
			t.Fatalf("help %q lacks %q", stderr, line)
		}
	}
	if count := strings.Count(stderr, "\n  -"); count != 29 {
		t.Fatalf("help lists %d flags", count)
	}
	_, stderr, err = pilotR3Run(t, "", "status", "--nope")
	if !strings.HasPrefix(stderr, "flag provided but not defined: -nope\nUsage of gsc-pilot status:\n") || err == nil {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
}

// pilotR3Record adds one reservation for pilot-test@staging and returns its
// provisioning time. Unless provisioned, the instance has no provisioning
// time; a positive readyAfter records readiness that long after
// provisioning; destroyed releases it afterwards.
func pilotR3Record(t *testing.T, repository *store.BoltStore, index int, provisioned bool, readyAfter time.Duration, destroyed bool) time.Time {
	t.Helper()
	operationID := fmt.Sprintf("%032x", index)
	instanceID := fmt.Sprintf("vm-%d", index)
	reservation, _, err := repository.ReserveCapacity(store.CapacityReservation{
		OperationID: operationID, ServerID: "pilot-test", EnvironmentID: "staging",
		CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-r3-" + instanceID,
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	var provisionedAt time.Time
	if provisioned {
		provisionedAt = reservation.CreatedAt
	}
	observedAt := time.Now().UTC()
	if observedAt.Before(reservation.CreatedAt) {
		observedAt = reservation.CreatedAt
	}
	if _, err := repository.ObserveCapacity(operationID, store.CapacityProviderObservation{
		Provider: reservation.Provider, ResourceKey: reservation.ResourceKey, ProviderInstanceID: instanceID,
		State: "present", ObservedAt: observedAt, ProvisionedAt: provisionedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if readyAfter > 0 {
		if _, err := repository.RecordCapacityReady("pilot-test", "staging", instanceID, provisionedAt.Add(readyAfter)); err != nil {
			t.Fatal(err)
		}
	}
	if destroyed {
		if _, err := repository.MarkCapacityDestroySent(operationID); err != nil {
			t.Fatal(err)
		}
		destroyedAt := time.Now().UTC()
		if destroyedAt.Before(observedAt) {
			destroyedAt = observedAt
		}
		if _, err := repository.ObserveCapacity(operationID, store.CapacityProviderObservation{
			Provider: reservation.Provider, ResourceKey: reservation.ResourceKey,
			ProviderInstanceID: instanceID, State: "destroyed", ObservedAt: destroyedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return provisionedAt
}

func pilotR3BootStatus(t *testing.T, statePath string, now time.Time) string {
	t.Helper()
	var output bytes.Buffer
	if err := showBootStatus("pilot-test", "staging", statePath, now, &output); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func TestPilotR3BootStatusOutputs(t *testing.T) {
	var output bytes.Buffer
	for _, args := range [][]string{{"Bad", "staging"}, {"pilot-test", "qa"}} {
		err := showBootStatus(args[0], args[1], filepath.Join(t.TempDir(), "x.db"), time.Now(), &output)
		if pilotR3ErrorText(err) != "a valid server ID, environment, and current time are required" {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if err := showBootStatus("pilot-test", "staging", filepath.Join(t.TempDir(), "x.db"), time.Time{}, &output); pilotR3ErrorText(err) != "a valid server ID, environment, and current time are required" {
		t.Fatal(err)
	}

	statePath := filepath.Join(t.TempDir(), "pilot.db")
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	pilotR3Record(t, repository, 1, true, 10*time.Second, true)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	got := pilotR3BootStatus(t, statePath, base)
	if got != "No controller-owned VM is currently recorded for pilot-test@staging.\nHistorical boot estimate unavailable (1 completed boot sample(s); 3 required).\n" {
		t.Fatalf("no current VM: %q", got)
	}

	repository, err = store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	pilotR3Record(t, repository, 2, true, 20*time.Second, true)
	pilotR3Record(t, repository, 3, true, 30*time.Second, true)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	got = pilotR3BootStatus(t, statePath, base)
	if got != "No controller-owned VM is currently recorded for pilot-test@staging.\nHistorical boot estimate: 38s (75th percentile plus 25% buffer; 3 samples).\n" {
		t.Fatalf("history estimate: %q", got)
	}

	// An unprovisioned current VM.
	repository, err = store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	pilotR3Record(t, repository, 4, false, 0, false)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	got = pilotR3BootStatus(t, statePath, base)
	if got != "Provider presence is confirmed, but no provisioning timestamp is available; Paper boot progress is indeterminate.\nHistorical boot estimate: 38s (75th percentile plus 25% buffer; 3 samples).\n" {
		t.Fatalf("unprovisioned: %q", got)
	}
}

func TestPilotR3BootStatusPendingAndReady(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		pilotR3Record(t, repository, i, true, time.Duration(i)*40*time.Second, true)
	}
	base := pilotR3Record(t, repository, 9, true, 0, false)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	got := pilotR3BootStatus(t, statePath, base.Add(60*time.Second))
	want := "Paper readiness is pending. Time since provisioning: 1m0s.\n" +
		"Estimated total: 2m30s; estimated time remaining: 1m30s.\n" +
		"Progress: [########------------] 40% (advisory; never reaches 100% before Paper readiness).\n"
	if got != want {
		t.Fatalf("pending:\n got %q\nwant %q", got, want)
	}
	got = pilotR3BootStatus(t, statePath, base.Add(-time.Minute))
	if !strings.HasPrefix(got, "Paper readiness is pending. Time since provisioning: 0s.\nEstimated total: 2m30s; estimated time remaining: 2m30s.\nProgress: [--------------------] 0% ") {
		t.Fatalf("clock skew: %q", got)
	}
	got = pilotR3BootStatus(t, statePath, base.Add(time.Hour))
	if !strings.HasPrefix(got, "Paper readiness is pending. Time since provisioning: 1h0m0s.\nEstimated total: 2m30s; estimated time remaining: 0s.\nProgress: [###################-] 99% ") {
		t.Fatalf("overdue: %q", got)
	}

	readyAt := base.Add(95 * time.Second)
	repository, err = store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RecordCapacityReady("pilot-test", "staging", "vm-9", readyAt); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	got = pilotR3BootStatus(t, statePath, base.Add(time.Hour))
	want = "Paper readiness was confirmed at " + readyAt.Format(time.RFC3339) + ".\n" +
		"Measured provision-to-Paper-ready time: 1m35s.\n" +
		"Progress: [####################] 100% (initial boot completed)\n"
	if got != want {
		t.Fatalf("ready:\n got %q\nwant %q", got, want)
	}

	repository, err = store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MarkCapacityDestroySent(fmt.Sprintf("%032x", 9)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	got = pilotR3BootStatus(t, statePath, base.Add(time.Hour))
	want = "Paper readiness was previously confirmed at " + readyAt.Format(time.RFC3339) + ".\n" +
		"Managed VM state: destroy_outcome_unknown. Current Paper readiness is not confirmed; boot progress is indeterminate.\n" +
		"Historical boot estimate: 1m59s (75th percentile plus 25% buffer; 4 samples).\n"
	if got != want {
		t.Fatalf("destroying:\n got %q\nwant %q", got, want)
	}
}

func TestPilotR3BootStatusCommandAndCapacityOutput(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	stdout, _, err := pilotR3Run(t, statePath, "boot-status", "--server-id", "pilot-test", "--environment", "testing")
	if err != nil || stdout != "No controller-owned VM is currently recorded for pilot-test@testing.\nHistorical boot estimate unavailable (0 completed boot sample(s); 3 required).\n" {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
	stdout, _, err = pilotR3Run(t, filepath.Join(t.TempDir(), "empty.db"), "capacity")
	if err != nil || stdout != "Managed VM capacity: 0/2 total.\nproduction: 0/1 slot(s)\nshared_nonproduction: 0/1 slot(s)\nNo managed VM reservations are recorded.\n" {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	pilotR3Record(t, repository, 1, false, 0, true)
	if _, _, err := repository.ReserveCapacity(store.CapacityReservation{
		OperationID: pilotR3OperationID, ServerID: "pilot-test", EnvironmentID: "staging",
		CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-r3-reserved",
	}, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = pilotR3Run(t, statePath, "capacity")
	want := "Managed VM capacity: 1/2 total.\nproduction: 0/1 slot(s)\nshared_nonproduction: 1/1 slot(s)\n" +
		"OPERATION_ID\tSERVER\tENVIRONMENT\tSTATE\tPROVIDER_INSTANCE_ID\tRESOURCE_LABEL\n" +
		fmt.Sprintf("%032x", 1) + "\tpilot-test\tstaging\tdestroyed\tvm-1\tgsc-r3-vm-1\n" +
		pilotR3OperationID + "\tpilot-test\tstaging\treserved\t-\tgsc-r3-reserved\n"
	if err != nil || stdout != want {
		t.Fatalf("err=%v\nstdout=%q\nwant  %q", err, stdout, want)
	}
}

func TestPilotR3HistoryOutput(t *testing.T) {
	home := pilotR3Home(t)
	stdout, _, err := pilotR3Run(t, "", "history")
	if err != nil || stdout != "No pilot operations have been recorded.\n" {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
	auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auditPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = pilotR3Run(t, "", "history")
	if err != nil || stdout != "No pilot operations have been recorded.\n" {
		t.Fatalf("empty: err=%v stdout=%q", err, stdout)
	}
	lines := `{"operation_id":"` + pilotR3OperationID + `","recorded_at":"2026-01-02T03:04:05.5Z","event":"started","action":"backup","target":"host","service":"minecraft-backup.service","detail":"a \"b\""}
{"operation_id":"` + pilotR3OperationID + `","recorded_at":"2026-01-02T03:04:06+01:00","event":"provision_confirmed","action":"provision","target":"pilot-test@staging","service":"vultr.instance.create"}
`
	if err := os.WriteFile(auditPath, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = pilotR3Run(t, "", "history")
	want := "RECORDED_AT\tOPERATION_ID\tACTION\tEVENT\tTARGET\tSERVICE\tDETAIL\n" +
		"2026-01-02T03:04:05.5Z\t" + pilotR3OperationID + "\tbackup\tstarted\thost\tminecraft-backup.service\t\"a \\\"b\\\"\"\n" +
		"2026-01-02T02:04:06Z\t" + pilotR3OperationID + "\tprovision\tprovision_confirmed\tpilot-test@staging\tvultr.instance.create\t-\n"
	if err != nil || stdout != want {
		t.Fatalf("err=%v\nstdout=%q\nwant  %q", err, stdout, want)
	}
	for content, wantErr := range map[string]string{
		"not json\n": "decode audit log " + auditPath + " line 1: invalid character 'o' in literal null (expecting 'u')",
		`{"operation_id":"` + pilotR3OperationID + `","recorded_at":"yesterday","event":"started","action":"backup","target":"host","service":"minecraft-backup.service"}` + "\n": "invalid timestamp in audit log " + auditPath + " line 1",
		`{"operation_id":"` + pilotR3OperationID + `","recorded_at":"2026-01-02T03:04:05Z","event":"started","action":"destroy","target":"host","service":"x"}` + "\n":            "invalid audit record in " + auditPath + " line 1",
	} {
		if err := os.WriteFile(auditPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pilotR3Run(t, "", "history"); pilotR3ErrorText(err) != wantErr {
			t.Fatalf("history error = %q, want %q", pilotR3ErrorText(err), wantErr)
		}
	}
}
