package bootstrap

import (
    "strings"
    "testing"
)

func TestPaperHealthProbeIsBundledAndCannotProvision(t *testing.T) {
    script, err := Render(Settings{
        ControllerIP: "203.0.113.10/32",
        MinecraftPort: 25566,
        VoiceChatPort: 24454,
        WasabiRemotePath: "example-bucket/server",
        Plan: "vc4-1-0150",
    })
    if err != nil {
        t.Fatal(err)
    }
    output := string(script)
    required := []string{
        "/usr/local/libexec/gsc/minecraft_probe.py",
        "minecraft-admin ALL=(root) NOPASSWD: /usr/local/libexec/gsc/minecraft_probe.py",
        "StartLimitIntervalSec=900",
        "StartLimitBurst=3",
        "Restart=on-failure",
        "PrivateTmp=true",
        "fatal_current_invocation",
        "journalctl",
    }
    for _, value := range required {
        if !strings.Contains(output, value) {
            t.Errorf("rendered bootstrap missing %q", value)
        }
    }
    if strings.Contains(output, "@@MINECRAFT_PROBE_PY@@") {
        t.Fatal("probe source marker was not rendered")
    }
    if strings.Contains(output, "PrivateTmp=true=true") {
        t.Fatal("invalid systemd PrivateTmp setting")
    }
}
