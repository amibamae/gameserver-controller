package bootstrap

import (
	"bytes"
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
