package adapterprocess

import "testing"

func TestParseEnvelopeChecksVersionBeforeStringID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		frame     string
		wantID    string
		wantError string
	}{
		{name: "version before id", frame: `{"jsonrpc":"1.0","id":2}`, wantError: `jsonrpc must be "2.0"`},
		{name: "missing id", frame: `{"jsonrpc":"2.0"}`, wantError: "id must be a string; notifications and numeric IDs are not supported"},
		{name: "numeric id", frame: `{"jsonrpc":"2.0","id":2}`, wantError: "id must be a string; notifications and numeric IDs are not supported"},
		{name: "empty id", frame: `{"jsonrpc":"2.0","id":""}`, wantError: "id must be a non-empty string"},
		{name: "valid id", frame: `{"jsonrpc":"2.0","id":"request-2"}`, wantID: "request-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, id, code, err := parseEnvelope([]byte(tc.frame))
			if tc.wantError == "" {
				if id != tc.wantID || code != 0 || err != nil {
					t.Fatalf("id=%q code=%d error=%v, want id=%q and success", id, code, err, tc.wantID)
				}
				return
			}
			if id != "" || code != codeInvalidRequest || err == nil || err.Error() != tc.wantError {
				t.Fatalf("id=%q code=%d error=%v, want no id and invalid request %q", id, code, err, tc.wantError)
			}
		})
	}
}
