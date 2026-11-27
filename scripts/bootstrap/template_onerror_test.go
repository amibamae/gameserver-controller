package bootstrap

import (
	"regexp"
	"strings"
	"testing"
)

// The failure handler runs as root on a VM that holds the rclone
// credentials, SSH host keys and /etc/shadow. It must only record the
// failure: the bootstrap log is already kept at $LOG, and archiving the
// filesystem would copy those secrets into /var/log and can fill the disk.
func TestFailureHandlerOnlyRecordsTheFailure(t *testing.T) {
	handler := between(t, renderedForTest(t), "on_error() {\n", "\n}\n")
	if line := regexp.MustCompile(`(?m)^\s*tar\s.*$`).FindString(handler); line != "" {
		t.Fatalf("the failure handler archives files: %q", strings.TrimSpace(line))
	}
	for _, want := range []string{
		`echo "$rc" > "$STATE_DIR/bootstrap-failed"`,
		`logger -t gsc-bootstrap "gsc bootstrap failed" 2>/dev/null || true`,
		`exit "$rc"`,
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("the failure handler is missing %q", want)
		}
	}
}
