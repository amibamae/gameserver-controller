//go:build !windows

package main

// Recovered CLI characterization uses temporary state and fake provider/SSH calls.
import (
	"github.com/amibamae/gameserver-controller/internal/store"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPilotR3ProvisionDryRunOutputAndNoSideEffects(t *testing.T) {
	home := pilotR3Home(t)
	t.Setenv("CREDENTIALS_DIRECTORY", filepath.Join(t.TempDir(), "missing"))
	fake := pilotR3InstallVultr(t, "")
	statePath := filepath.Join(t.TempDir(), "state", "pilot.db")
	stdout, stderr, err := pilotR3Run(t, statePath, provisionArgs()...)
	pilotR3RequireNoError(t, err)
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
		t.Setenv("USERPROFILE", "")
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
	t.Run("credential directory missing", pilotR3ProvisionStage0)
	t.Run("controller key missing", pilotR3ProvisionStage1)
	t.Run("capacity store unavailable", pilotR3ProvisionStage2)
}

func pilotR3ProvisionStage0(t *testing.T) {
	requested := "provision_requested|provision|pilot-test@staging|vultr.instance.create|region=ewr plan=vc2-1c-2gb os_id=362 startup_script_id=script-123 startup_script_sha256=" + pilotR3Digest()

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
}

func pilotR3ProvisionStage1(t *testing.T) {
	requested := "provision_requested|provision|pilot-test@staging|vultr.instance.create|region=ewr plan=vc2-1c-2gb os_id=362 startup_script_id=script-123 startup_script_sha256=" + pilotR3Digest()

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
}

func pilotR3ProvisionStage2(t *testing.T) {
	requested := "provision_requested|provision|pilot-test@staging|vultr.instance.create|region=ewr plan=vc2-1c-2gb os_id=362 startup_script_id=script-123 startup_script_sha256=" + pilotR3Digest()

	home := pilotR3Home(t)
	pilotR3Credentials(t, true, true)
	fake := pilotR3InstallVultr(t, "")
	blocker := filepath.Join(t.TempDir(), "file")
	pilotR3RequireNoError(t, os.WriteFile(blocker, nil, 0o600))
	stdout, stderr, err := pilotR3Run(t, filepath.Join(blocker, "pilot.db"), pilotR3ExecuteArgs()...)
	if !strings.HasPrefix(pilotR3ErrorText(err), "open capacity store: ") || strings.Contains(pilotR3ErrorText(err), "audit") || stdout != "" || stderr != "" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	pilotR3Equal(t, "audit", pilotR3AuditSummary(pilotR3Audit(t, home)), []string{requested,
		"provision_failed|provision|pilot-test@staging|vultr.instance.create|capacity store unavailable"})
	if len(fake.recorded()) != 0 {
		t.Fatal("called Vultr")
	}
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
				pilotR3RequireNoError(t, err)
				if _, _, err := repository.ReserveCapacity(store.CapacityReservation{
					OperationID: "fedcba9876543210fedcba9876543210", ServerID: "other", EnvironmentID: "testing",
					CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-managed-other",
				}, 2, 1); err != nil {
					t.Fatal(err)
				}
				pilotR3RequireNoError(t, repository.Close())
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
	pilotR3RequireNoError(t, err)
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
