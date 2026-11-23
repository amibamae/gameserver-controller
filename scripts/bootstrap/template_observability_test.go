package bootstrap

import (
	"strings"
	"testing"
)

func TestVictoriaLogsInstalledDisabledAndSSHConfigured(t *testing.T) {
	rendered, err := Render(Settings{
		ControllerIP:     "203.0.113.10/32",
		MinecraftPort:    25565,
		VoiceChatPort:    24454,
		WasabiRemotePath: "example-bucket/backups",
		Plan:             "vc2-1-0150",
	})
	if err != nil {
		t.Fatal(err)
	}
	script := string(rendered)
	required := []string{
		"gsc-victorialogs.service",
		"gsc-victorialogs.timer",
		"/usr/local/libexec/gsc/victorialogs_export.py",
		"/usr/local/sbin/gsc-configure-victorialogs",
		"minecraft-admin ALL=(root) NOPASSWD: /usr/local/sbin/gsc-configure-victorialogs",
		"LoadCredential=token:/etc/gsc-observability/victorialogs.token",
		"DynamicUser=yes",
		"GSC_VL_RAW_GAME_LOGS=0",
		"GSC_VL_PLAYER_LOG_CONSENT=0",
	}
	for _, value := range required {
		if !strings.Contains(script, value) {
			t.Errorf("rendered bootstrap missing %q", value)
		}
	}
	for _, prohibited := range []string{"@@VICTORIALOGS_EXPORT_PY@@",
		"@@VICTORIALOGS_CONFIGURE_PY@@", "systemctl enable --now gsc-victorialogs.timer"} {
		if strings.Contains(script, prohibited) {
			t.Errorf("bootstrap must not contain %q", prohibited)
		}
	}
}
