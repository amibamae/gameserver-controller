package bootstrap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, value := range []string{"@@CONTROLLER_IP@@", "@@MINECRAFT_PORT@@", "@@VOICE_CHAT_PORT@@", "@@WASABI_REMOTE_PATH@@"} {
		if strings.Contains(text, value) {
			t.Fatalf("unrendered placeholder %q remains", value)
		}
	}
	for _, value := range []string{"203.0.113.10/32", "25565", "24454", "wasabi:example-bucket/backups"} {
		if !strings.Contains(text, value) {
			t.Fatalf("rendered script is missing %q", value)
		}
	}
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
	valid := Settings{ControllerIP: "203.0.113.10/32", MinecraftPort: 25565, VoiceChatPort: 24454, WasabiRemotePath: "example-bucket/backups"}
	cases := map[string]func(*Settings){
		"shell injection in CIDR": func(s *Settings) { s.ControllerIP = "203.0.113.10/32; touch /tmp/pwned" },
		"private CIDR":            func(s *Settings) { s.ControllerIP = "10.0.0.1/32" },
		"too broad CIDR":          func(s *Settings) { s.ControllerIP = "203.0.0.0/15" },
		"private address":         func(s *Settings) { s.ControllerIP = "192.168.1.10" },
		"IPv6 address":            func(s *Settings) { s.ControllerIP = "2001:db8::1" },
		"empty controller IP":     func(s *Settings) { s.ControllerIP = "" },
		"zero Minecraft port":     func(s *Settings) { s.MinecraftPort = 0 },
		"duplicate ports":         func(s *Settings) { s.VoiceChatPort = s.MinecraftPort },
		"shell injection in path": func(s *Settings) { s.WasabiRemotePath = "bucket/$(id)" },
		"parent traversal":        func(s *Settings) { s.WasabiRemotePath = "bucket/../private" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if _, err := Render(input); err == nil {
				t.Fatal("unsafe settings were accepted")
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
	for _, check := range [][]string{{"bash", "-n"}, {"shellcheck", "-S", "warning"}} {
		if _, err := exec.LookPath(check[0]); err != nil {
			continue
		}
		path := filepath.Join(t.TempDir(), "rotate.sh")
		if err := os.WriteFile(path, []byte(rotator+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(check[0], append(check[1:], path)...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", check[0], err, out)
		}
	}
	if _, err := exec.LookPath("bash"); err == nil {
		path := filepath.Join(t.TempDir(), "bootstrap.sh")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
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
