package bootstrap

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRenderReplacesEveryDeploymentValue(t *testing.T) {
	settings := Settings{
		ControllerIP: "203.0.113.10/32", MinecraftPort: 25565,
		VoiceChatPort: 24454, WasabiRemotePath: "example-bucket/backups",
	}
	script, err := Render(settings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	if i := strings.Index(text, "@@"); i >= 0 {
		t.Fatalf("unrendered placeholder remains: %q", text[i:min(i+40, len(text))])
	}
	for _, value := range []string{"203.0.113.10/32", "25565", "24454", "wasabi:example-bucket/backups"} {
		if !strings.Contains(text, value) {
			t.Fatalf("rendered script is missing %q", value)
		}
	}
	// Anything before the shebang, such as a UTF-8 byte-order mark, stops the
	// script from running at all.
	if !bytes.HasPrefix(script, []byte("#!/usr/bin/env bash\n")) {
		t.Fatal("rendered asset must be a bash script")
	}
}

func TestRenderAcceptsBareControllerIP(t *testing.T) {
	settings := Settings{ControllerIP: "203.0.113.10", MinecraftPort: 25565, VoiceChatPort: 24454, WasabiRemotePath: "example-bucket/backups"}
	script, err := Render(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `CONTROLLER_IP="203.0.113.10"`) {
		t.Fatal("rendered script does not carry the bare controller IP")
	}
}

func TestRenderRejectsUnsafeValues(t *testing.T) {
	const (
		badIP    = "invalid bootstrap settings: controller IP must be a public IPv4 address or CIDR"
		tooBroad = "invalid bootstrap settings: controller IP CIDR must be /16 or narrower"
		badPorts = "invalid bootstrap settings: Minecraft and voice chat ports must be distinct nonzero ports"
		badPath  = "invalid bootstrap settings: Wasabi remote path must be a bucket name with an optional safe prefix"
	)
	valid := Settings{ControllerIP: "203.0.113.10/32", MinecraftPort: 25565, VoiceChatPort: 24454, WasabiRemotePath: "example-bucket/backups"}
	cases := map[string]struct {
		mutate func(*Settings)
		want   string
	}{
		"shell injection in CIDR": {func(s *Settings) { s.ControllerIP = "203.0.113.10/32; touch /tmp/pwned" }, badIP},
		"private CIDR":            {func(s *Settings) { s.ControllerIP = "10.0.0.1/32" }, badIP},
		"too broad CIDR":          {func(s *Settings) { s.ControllerIP = "203.0.0.0/15" }, tooBroad},
		"private address":         {func(s *Settings) { s.ControllerIP = "192.168.1.10" }, badIP},
		"loopback address":        {func(s *Settings) { s.ControllerIP = "127.0.0.1" }, badIP},
		"IPv6 address":            {func(s *Settings) { s.ControllerIP = "2001:db8::1" }, badIP},
		"IPv6 CIDR":               {func(s *Settings) { s.ControllerIP = "2001:db8::/48" }, badIP},
		"empty controller IP":     {func(s *Settings) { s.ControllerIP = "" }, badIP},
		"zero Minecraft port":     {func(s *Settings) { s.MinecraftPort = 0 }, badPorts},
		"zero voice chat port":    {func(s *Settings) { s.VoiceChatPort = 0 }, badPorts},
		"duplicate ports":         {func(s *Settings) { s.VoiceChatPort = s.MinecraftPort }, badPorts},
		"shell injection in path": {func(s *Settings) { s.WasabiRemotePath = "bucket/$(id)" }, badPath},
		"parent traversal":        {func(s *Settings) { s.WasabiRemotePath = "bucket/../private" }, badPath},
		"empty path segment":      {func(s *Settings) { s.WasabiRemotePath = "bucket//private" }, badPath},
		"trailing slash":          {func(s *Settings) { s.WasabiRemotePath = "bucket/" }, badPath},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := valid
			tc.mutate(&input)
			_, err := Render(input)
			if err == nil || err.Error() != tc.want || !errors.Is(err, ErrInvalidSettings) {
				t.Fatalf("Render() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func renderedForTest(t *testing.T) string {
	t.Helper()
	script, err := Render(Settings{ControllerIP: "203.0.113.10", MinecraftPort: 25565, VoiceChatPort: 24454, WasabiRemotePath: "example-bucket/backups"})
	if err != nil {
		t.Fatal(err)
	}
	return string(script)
}

// SSH must be reachable only from the controller from the first firewall
// rule on, never open to every address while the bootstrap runs.
func TestSSHIsNeverOpenToEveryAddress(t *testing.T) {
	text := renderedForTest(t)
	if strings.Contains(text, "ufw allow 22/tcp") || strings.Contains(text, "allow ssh") {
		t.Fatal("the bootstrap opens SSH to every address")
	}
	if strings.Count(text, "to any port 22") < 2 || !strings.Contains(text, "from \"$CONTROLLER_IP\" \\\n    to any port 22") {
		t.Fatal("the initial firewall does not limit SSH to the controller")
	}
}

func TestHostKeySwapAccountCanOnlyRunTheSwap(t *testing.T) {
	text := renderedForTest(t)
	for _, want := range []string{
		`printf 'restrict,command="sudo -n %s" %s\n' "$HOST_KEY_ROTATOR" "$KEY"`,
		`printf '%s ALL=(root) NOPASSWD: %s\n' "$HOSTKEY_USER" "$HOST_KEY_ROTATOR"`,
		"--shell /bin/sh",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("swap account setup is missing %q", want)
		}
	}
	rotator := between(t, text, "cat > \"$HOST_KEY_ROTATOR\" <<'EOF_ROTATE'\n", "\nEOF_ROTATE\n")
	lines := strings.Split(strings.TrimSpace(rotator), "\n")
	if last := lines[len(lines)-1]; last != `awk '{print $1 " " $2}' /etc/ssh/ssh_host_ed25519_key.pub` {
		t.Fatalf("the new public key must be the swap's last output, got %q", last)
	}
	for _, want := range []string{"flock -n 9", `if [[ -e "$MARKER" ]]`, "rm -f /var/lib/cloud/instances/*/user-data.txt*",
		`rm -f "/home/$HOSTKEY_USER/.ssh/authorized_keys" /etc/sudoers.d/gsc-hostkey`, "HostKey /etc/ssh/ssh_host_ed25519_key", "sshd -t"} {
		if !strings.Contains(rotator, want) {
			t.Fatalf("swap script is missing %q", want)
		}
	}
	// Everything except the final key line must go to stderr or files.
	for _, line := range lines[:len(lines)-1] {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "echo ") && !strings.Contains(trimmed, ">") {
			t.Fatalf("swap script writes extra stdout: %q", line)
		}
	}
	lintShell(t, "rotate.sh", rotator+"\n", shellLinters)
	lintShell(t, "bootstrap.sh", text, [][]string{{"bash", "-n"}})
}

// shellLinters check a script the bootstrap installs. A linter that is not
// installed is skipped, so the tests still run where it is missing.
var shellLinters = [][]string{{"bash", "-n"}, {"shellcheck", "-S", "warning"}}

// lintShell writes script to a temporary file named name and runs each
// installed linter on it.
func lintShell(t *testing.T, name, script string, linters [][]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, linter := range linters {
		if _, err := exec.LookPath(linter[0]); err != nil {
			continue
		}
		if out, err := exec.Command(linter[0], append(linter[1:], path)...).CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", linter[0], name, err, out)
		}
	}
}

func between(t *testing.T, text, start, end string) string {
	t.Helper()
	i := strings.Index(text, start)
	if i < 0 {
		t.Fatalf("missing %q", start)
	}
	rest := text[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("missing %q", end)
	}
	return rest[:j]
}

// The swap account is enabled only after final validation, as the last step
// before the completion marker, with the sudo rule before the login key: an
// early swap must not break the bootstrap, and a login must always be able
// to run the swap.
func TestSwapAccountIsEnabledLastAndInOrder(t *testing.T) {
	text := renderedForTest(t)
	validation := strings.Index(text, `section "Final validation"`)
	enable := strings.Index(text, `section "Enabling the host key swap account"`)
	sudoRule := strings.Index(text, "mv -f /etc/sudoers.d/.gsc-hostkey.new /etc/sudoers.d/gsc-hostkey")
	loginKey := strings.Index(text, `mv -f "/home/$HOSTKEY_USER/.ssh/authorized_keys.new"`)
	marker := strings.Index(text, `cat > "$BOOTSTRAP_MARKER"`)
	if validation < 0 || enable < validation || sudoRule < enable || loginKey < sudoRule || marker < loginKey {
		t.Fatalf("order: validation=%d enable=%d sudo=%d key=%d marker=%d", validation, enable, sudoRule, loginKey, marker)
	}
	if strings.Count(text, "restrict,command=") != 1 {
		t.Fatal("the swap login key must be written in exactly one place")
	}
	if strings.Contains(text[enable:marker], "test -f \"/home/$HOSTKEY_USER") {
		t.Fatal("nothing after enabling may depend on the swap account's files")
	}
}

// Software comes from Debian packages, which apt verifies. The bootstrap runs
// as root on first boot, so it must not download programs itself, and its
// sudo rules name exact commands, never a wildcard.
func TestBootstrapFetchesNoProgramsAndGrantsNoWildcardSudo(t *testing.T) {
	text := renderedForTest(t)
	if line := regexp.MustCompile(`(?m)^\s*(?:curl|wget)\s+[^\\\s].*$`).FindString(text); line != "" {
		t.Fatalf("the bootstrap downloads something itself: %q", strings.TrimSpace(line))
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "NOPASSWD:") && strings.Contains(line, "*") {
			t.Fatalf("sudo rule allows any arguments: %q", strings.TrimSpace(line))
		}
	}
}

func startScript(t *testing.T) string {
	t.Helper()
	return between(t, renderedForTest(t), "cat > /usr/local/sbin/minecraft-start.sh <<'EOF_START'\n", "\nEOF_START\n") + "\n"
}

// The start script sizes the heap from the VM's memory when the server starts,
// so the one shared startup script fits every plan.
func TestStartScriptPassesTheMeasuredHeap(t *testing.T) {
	start := startScript(t)
	for _, want := range []string{
		`HEAP_MIB="$(heap_mib /proc/meminfo)"`,
		"exec /usr/bin/java \\\n    -Xms\"$(( HEAP_MIB / 2 ))M\" \\\n    -Xmx\"${HEAP_MIB}M\" \\\n    -jar \"${JARS[0]}\" \\\n    --nogui",
	} {
		if !strings.Contains(start, want) {
			t.Fatalf("start script is missing %q", want)
		}
	}
	if strings.Count(start, "-Xmx") != 1 || strings.Count(start, "-Xms") != 1 {
		t.Fatal("the start script must set the heap in exactly one place")
	}
	lintShell(t, "minecraft-start.sh", start, shellLinters)
}

// heap_mib keeps a quarter of the memory, and at least 768 MiB, for
// everything but the heap, and refuses a VM too small for the server.
func TestStartScriptSizesHeapFromMemory(t *testing.T) {
	for _, tool := range []string{"bash", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	heapMiB := "heap_mib() {\n" + between(t, startScript(t), "heap_mib() {\n", "\n}\n") + "\n}\n"
	tooSmall := "This VM has %s MiB of memory; the Minecraft server needs at least 1280 MiB\n"
	cases := []struct {
		name, meminfo, stdout, stderr string
	}{
		{"2 GB plan keeps the 768 MiB floor", "MemTotal:        2013572 kB\nMemFree:          912344 kB\n", "1198\n", ""},
		{"4 GB plan keeps a quarter", "MemTotal:        4017024 kB\n", "2942\n", ""},
		{"8 GB plan keeps a quarter", "MemTotal:        8136508 kB\n", "5959\n", ""},
		{"smallest VM that fits", "MemTotal:        1310720 kB\n", "512\n", ""},
		{"VM 1 MiB too small", "MemTotal:        1309696 kB\n", "", strings.Replace(tooSmall, "%s", "1279", 1)},
		{"1 GB plan", "MemTotal:         985000 kB\n", "", strings.Replace(tooSmall, "%s", "961", 1)},
		{"no MemTotal line", "MemFree:          912344 kB\n", "", "Could not read MemTotal from @PATH@\n"},
		{"MemTotal not a number", "MemTotal:        lots kB\n", "", "Could not read MemTotal from @PATH@\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "meminfo")
			if err := os.WriteFile(path, []byte(tc.meminfo), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			command := exec.Command("bash", "-c", heapMiB+`heap_mib "$1"`, "bash", path)
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			wantErr := strings.ReplaceAll(tc.stderr, "@PATH@", path)
			if stdout.String() != tc.stdout || stderr.String() != wantErr || (err == nil) != (tc.stdout != "") {
				t.Fatalf("heap_mib: stdout %q stderr %q err %v; want stdout %q stderr %q", stdout.String(), stderr.String(), err, tc.stdout, wantErr)
			}
		})
	}
}
