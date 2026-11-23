//go:build !windows

package main

// Recovered CLI characterization uses temporary state and fake provider/SSH calls.
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

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
	pilotR3RequireNoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte(script.String()), 0o700))
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
	pilotR3RequireNoError(t, err)
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
}

func TestPilotR3BackupRequiresAuditHome(t *testing.T) {
	logPath := pilotR3FakeSSH(t, nil, 0, "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	_, _, err := pilotR3Run(t, "", "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
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
			pilotR3CheckBackupAudit(t, records, test.wantAudit, operationID, !strings.HasPrefix(test.wantErr, "operation"))
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

func pilotR3CheckBackupAudit(t *testing.T, records []auditRecord, expected []string, operationID string, started bool) {
	t.Helper()
	wantAudit := expected
	if started {
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
}
