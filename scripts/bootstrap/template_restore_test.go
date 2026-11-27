package bootstrap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const syncRefusal = "ERROR: the Wasabi restore has not completed on this VM.\n" +
	"Synchronizing now would replace the Wasabi copy with incomplete local data.\n"

// minecraft-sync.sh mirrors the local server directory to Wasabi with
// rclone sync, which deletes remote files that are missing locally. Before
// the restore has completed, the local directory is empty or partial, so a
// sync would destroy the authoritative copy. It must refuse to run.
func TestSyncRefusesBeforeTheRestoreCompleted(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	cases := []struct {
		name     string
		restored bool
		wantErr  string
		wantSync bool
	}{
		{"restore not completed", false, syncRefusal, false},
		{"restore completed", true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := newSyncSandbox(t, tc.restored)
			stderr, err := sandbox.run(t)
			if tc.wantSync != (err == nil) {
				t.Fatalf("sync err=%v; want success=%v\nstderr:\n%s", err, tc.wantSync, stderr)
			}
			if tc.wantErr != "" && stderr != tc.wantErr {
				t.Fatalf("sync stderr=%q; want %q", stderr, tc.wantErr)
			}
			if synced := strings.Contains(sandbox.rcloneCalls(t), "sync "); synced != tc.wantSync {
				t.Fatalf("rclone sync invoked=%v; want %v", synced, tc.wantSync)
			}
		})
	}
	lintShell(t, "minecraft-sync.sh", syncScript(t), shellLinters)
}

func syncScript(t *testing.T) string {
	t.Helper()
	return between(t, renderedForTest(t), "cat > /usr/local/sbin/minecraft-sync.sh <<'EOF_SYNC'\n", "\nEOF_SYNC\n") + "\n"
}

// syncSandbox runs the sync script against temporary directories, with
// stubbed systemctl and rclone.
type syncSandbox struct {
	script, calls string
}

func newSyncSandbox(t *testing.T, restored bool) syncSandbox {
	t.Helper()
	root := filepath.ToSlash(t.TempDir())
	state, server := root+"/state", root+"/server"
	for _, dir := range []string{state, server} {
		mustWrite(t, dir+"/.keep", "")
	}
	config := root + "/rclone.conf"
	mustWrite(t, config, "[wasabi]\n")
	if restored {
		mustWrite(t, state+"/restore-complete", "")
	}
	calls := root + "/rclone-calls"
	script := strings.NewReplacer(
		"/var/lib/minecraft-bootstrap", state,
		"/opt/minecraft/vanilla_no_grief", server,
		"/root/.config/rclone/rclone.conf", config,
	).Replace(syncScript(t))
	// The stubs shadow the real commands: systemctl reports the server
	// stopped, and rclone only records how it was called.
	stubs := "systemctl() { return 3; }\nrclone() { echo \"$*\" >> '" + calls + "'; }\n"
	script = strings.Replace(script, "set -Eeuo pipefail\n", "set -Eeuo pipefail\n"+stubs, 1)
	mustWrite(t, root+"/minecraft-sync.sh", script)
	return syncSandbox{script: root + "/minecraft-sync.sh", calls: calls}
}

func (s syncSandbox) run(t *testing.T) (string, error) {
	t.Helper()
	command := exec.Command("bash", s.script)
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = &bytes.Buffer{}, &stderr
	err := command.Run()
	return stderr.String(), err
}

func (s syncSandbox) rcloneCalls(t *testing.T) string {
	t.Helper()
	calls, err := os.ReadFile(s.calls)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(calls)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
