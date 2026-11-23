package main

import (
	"bytes"
	"testing"
)

func TestBackupCommandPreviewAndConfirmation(t *testing.T) {
	const preview = "Dry run: would check minecraft-backup.service on server.example, start it, and inspect the resulting systemd invocation.\n" +
		"The Paper service remains online; this uses the existing backup service.\n" +
		"To run it, add --execute --confirm-host server.example\n"
	for _, test := range []struct {
		name              string
		args              []string
		output, errorText string
	}{
		{name: "dry run", output: preview},
		{name: "mismatched confirmation", args: []string{"--execute", "--confirm-host", "other.example"}, errorText: "backup not started: --confirm-host must exactly match --host"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(append([]string{"backup", "--host", "server.example"}, test.args...), &stdout, &stderr)
			assertBackupCommandError(t, err, test.errorText)
			if stdout.String() != test.output || stderr.Len() != 0 {
				t.Fatalf("stdout = %q, stderr = %q, want stdout %q and empty stderr", stdout.String(), stderr.String(), test.output)
			}
		})
	}
}

func assertBackupCommandError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}
