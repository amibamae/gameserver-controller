//go:build !windows

package main

// Recovered CLI characterization uses temporary state and fake provider/SSH calls.
import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		{"unknown action", []string{"destroy"}, `unknown action "destroy"; expected status, backup, history, boot-status, provision, adopt, handoff, observability, register-ssh-key, or capacity`},
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
		{"adopt os id", []string{"adopt", "--os-id", "abc"}, "parse Vultr OS ID: invalid Vultr API request: provider OS ID must be a positive integer"},
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
	if !errors.Is(err, flag.ErrHelp) || stdout != "" || !strings.HasPrefix(stderr, "Usage of gsc-pilot status:\n") {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	pilotR3CheckUsageFlags(t, stderr)
	_, stderr, err = pilotR3Run(t, "", "status", "--nope")
	if !strings.HasPrefix(stderr, "flag provided but not defined: -nope\nUsage of gsc-pilot status:\n") || err == nil {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
}

func TestPilotR3HistoryOutput(t *testing.T) {
	home := pilotR3Home(t)
	stdout, _, err := pilotR3Run(t, "", "history")
	if err != nil || stdout != "No pilot operations have been recorded.\n" {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
	auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
	pilotR3RequireNoError(t, os.MkdirAll(filepath.Dir(auditPath), 0o700))
	pilotR3RequireNoError(t, os.WriteFile(auditPath, nil, 0o600))
	stdout, _, err = pilotR3Run(t, "", "history")
	if err != nil || stdout != "No pilot operations have been recorded.\n" {
		t.Fatalf("empty: err=%v stdout=%q", err, stdout)
	}
	lines := `{"operation_id":"` + pilotR3OperationID + `","recorded_at":"2026-01-02T03:04:05.5Z","event":"started","action":"backup","target":"host","service":"minecraft-backup.service","detail":"a \"b\""}
{"operation_id":"` + pilotR3OperationID + `","recorded_at":"2026-01-02T03:04:06+01:00","event":"provision_confirmed","action":"provision","target":"pilot-test@staging","service":"vultr.instance.create"}
`
	pilotR3RequireNoError(t, os.WriteFile(auditPath, []byte(lines), 0o600))
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
		pilotR3RequireNoError(t, os.WriteFile(auditPath, []byte(content), 0o600))
		if _, _, err := pilotR3Run(t, "", "history"); pilotR3ErrorText(err) != wantErr {
			t.Fatalf("history error = %q, want %q", pilotR3ErrorText(err), wantErr)
		}
	}
}
