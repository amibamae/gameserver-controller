package vultrapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCreateStartupScriptEncodesSourceAndValidatesProviderResponse(t *testing.T) {
	const source = "#!/bin/sh\nset -eu\nprintf '%s\\n' ready\n"
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v2/startup-scripts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Fatalf("authorization header was not set")
		}
		var body struct {
			Name   string `json:"name"`
			Type   string `json:"type"`
			Script string `json:"script"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Name != "controller-bootstrap-test" || body.Type != "boot" {
			t.Fatalf("unexpected create payload metadata: %+v", body)
		}
		decoded, err := base64.StdEncoding.DecodeString(body.Script)
		if err != nil || string(decoded) != source {
			t.Fatalf("request script was not base64-encoded source: %q, %v", decoded, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"startup_script": StartupScript{
			ID: "startup-script-id", Name: body.Name, Type: body.Type, Script: body.Script,
		}})
	})
	created, err := client.CreateStartupScript(context.Background(), []byte("synthetic-token"), StartupScriptRequest{
		Name: "controller-bootstrap-test", Script: []byte(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "startup-script-id" || calls != 1 {
		t.Fatalf("unexpected create result: %+v, calls=%d", created, calls)
	}
}

func TestUpdateStartupScriptPatchesOnlyExactID(t *testing.T) {
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPatch || r.URL.Path != "/v2/startup-scripts/script-123" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Name   string `json:"name"`
			Type   string `json:"type"`
			Script string `json:"script"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(body.Script)
		if err != nil || string(decoded) != "#!/bin/sh\nexit 0\n" {
			t.Fatalf("unexpected patched script source: %q, %v", decoded, err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if err := client.UpdateStartupScript(context.Background(), []byte("synthetic-token"), "script-123", StartupScriptRequest{
		Name: "controller-bootstrap-test", Type: "boot", Script: []byte("#!/bin/sh\nexit 0\n"),
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("PATCH calls=%d, want 1", calls)
	}
}

func TestGetStartupScriptReadsExactIDAndValidatesBase64(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/startup-scripts/script-123" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"startup_script": StartupScript{
			ID: "script-123", Name: "controller-bootstrap-test", Type: "boot",
			Script: base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n")),
		}})
	})
	script, err := client.GetStartupScript(context.Background(), []byte("synthetic-token"), "script-123")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(script.Script)
	if err != nil || string(decoded) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("unexpected startup script: %q, %v", decoded, err)
	}
}

func TestStartupScriptRejectsInvalidInputBeforeRequest(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("invalid input sent an HTTP request: %s %s", r.Method, r.URL.Path)
	})
	for _, input := range []StartupScriptRequest{
		{Name: "", Script: []byte("#!/bin/sh")},
		{Name: "name", Type: "unknown", Script: []byte("#!/bin/sh")},
		{Name: "name", Script: nil},
		{Name: "name", Script: []byte{0xff}},
		{Name: "name", Script: []byte("#!/bin/sh\x00exit 0")},
	} {
		if _, err := client.CreateStartupScript(context.Background(), []byte("synthetic-token"), input); err == nil {
			t.Fatalf("invalid startup script request was accepted: %+v", input)
		}
	}
	if err := client.UpdateStartupScript(context.Background(), []byte("synthetic-token"), "../other", StartupScriptRequest{
		Name: "name", Script: []byte("#!/bin/sh"),
	}); err == nil || !strings.Contains(err.Error(), "invalid Vultr API request") {
		t.Fatalf("invalid script ID returned %v", err)
	}
}
