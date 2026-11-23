package pilothandoff

import (
	"context"
	"strings"
	"testing"
)

func TestVictoriaLogsEndpointValidation(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://logs.example/insert/jsonline", true},
		{"https://logs.example:443/insert/jsonline", true},
		{"http://logs.example/insert/jsonline", false},
		{"https://name:secret@logs.example/insert/jsonline", false},
		{"https://logs.example/insert/jsonline?token=bad", false},
		{"https://logs.example/insert/jsonline#fragment", false},
		{"https://logs.example/other", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := validVictoriaLogsEndpoint(tc.url); got != tc.want {
			t.Errorf("validVictoriaLogsEndpoint(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestVictoriaLogsRejectsUnsafePrivacyBeforeConnecting(t *testing.T) {
	runner := Runner{}
	for _, test := range []struct{ name, privacy, endpoint, message string }{
		{"raw", "raw", "https://logs.example/insert/jsonline", "observability privacy mode must be lifecycle or disabled"},
		{"HTTP", "lifecycle", "http://logs.example/insert/jsonline", "observability endpoint must be HTTPS /insert/jsonline"},
		{"disabled endpoint", "disabled", "https://logs.example/insert/jsonline", "disabled observability must not specify an endpoint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runner.ConfigureVictoriaLogs(context.Background(), VictoriaLogsConfig{
				PrivacyMode: test.privacy, Endpoint: test.endpoint,
			})
			if err == nil || err.Error() != test.message {
				t.Fatalf("ConfigureVictoriaLogs error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestVictoriaLogsTokenPatternRejectsLineBreaks(t *testing.T) {
	for _, token := range []string{"short", "bad\n" + strings.Repeat("x", 18), "bad space" + strings.Repeat("x", 18)} {
		if logTokenPattern.Match([]byte(token)) {
			t.Fatalf("accepted unsafe token %q", token)
		}
	}
	if !logTokenPattern.Match([]byte(strings.Repeat("x", 32))) {
		t.Fatal("rejected valid token")
	}
}
