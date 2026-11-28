package vultrhosting

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

func TestInspectMapsProviderStates(t *testing.T) {
	cases := map[string]struct {
		status, power, server, want string
	}{
		"running ok":        {"active", "running", "ok", "active"},
		"running booting":   {"active", "running", "installingbooting", "creating"},
		"running none":      {"active", "running", "none", "unknown"},
		"running no server": {"active", "running", "", "unknown"},
		"running locked":    {"active", "running", "locked", "unknown"},
		"stopped":           {"active", "stopped", "none", "stopped"},
		"stopped locked":    {"active", "stopped", "locked", "unknown"},
		"pending":           {"pending", "", "", "creating"},
		"odd power":         {"active", "restarting", "ok", "unknown"},
		"suspended":         {"suspended", "stopped", "ok", "unknown"},
		"resizing":          {"resizing", "running", "ok", "unknown"},
		"empty status":      {"", "running", "ok", "unknown"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{instance: vultrapi.Instance{ID: "inst-1", Region: "ewr", Plan: "vc2-1c-1gb", Status: test.status, PowerStatus: test.power, ServerStatus: test.server}}
			output, err := invoke(t, testRegistry(t, reader, fakeCredentials{}), capabilityInspect, inspectInputJSON("inst-1"))
			if err != nil {
				t.Fatal(err)
			}
			if output["state"] != test.want || output["region_id"] != "ewr" || output["plan_id"] != "vc2-1c-1gb" || output["observed_at"] != "2026-11-27T12:00:00Z" {
				t.Fatalf("output = %v", output)
			}
			if reader.token != "test-token" {
				t.Fatalf("token = %q", reader.token)
			}
		})
	}
}

func TestInspectTreatsOnlyNotFoundAsAnObservation(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		reader := &fakeReader{err: fmt.Errorf("%w: %w", vultrapi.ErrProviderReject, vultrapi.StatusError(status))}
		_, err := invoke(t, testRegistry(t, reader, fakeCredentials{}), capabilityInspect, inspectInputJSON("inst-1"))
		if !strings.Contains(errorText(err), "Vultr instance inspection failed; the instance state is unknown") {
			t.Fatalf("HTTP %d: err = %v, want a retryable error", status, err)
		}
	}
	reader := &fakeReader{err: fmt.Errorf("%w: %w", vultrapi.ErrRequestFailed, vultrapi.StatusError(503))}
	if _, err := invoke(t, testRegistry(t, reader, fakeCredentials{}), capabilityInspect, inspectInputJSON("inst-1")); err == nil {
		t.Fatal("HTTP 503 was reported as an observation")
	}
}

func TestInspectReportsNotFoundAsUnknown(t *testing.T) {
	reader := &fakeReader{err: fmt.Errorf("%w: %w", vultrapi.ErrProviderReject, vultrapi.StatusError(404))}
	output, err := invoke(t, testRegistry(t, reader, fakeCredentials{}), capabilityInspect, inspectInputJSON("gone"))
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, _ := output["safe_diagnostics"].([]any)
	if output["state"] != "unknown" || len(diagnostics) != 1 || diagnostics[0] != notReturnedDiagnostic {
		t.Fatalf("output = %v", output)
	}
}

func TestInspectOmitsOversizedProviderIDs(t *testing.T) {
	long := strings.Repeat("r", 201)
	reader := &fakeReader{instance: vultrapi.Instance{ID: "inst-1", Region: long, Plan: long, Status: "active", PowerStatus: "running"}}
	output, err := invoke(t, testRegistry(t, reader, fakeCredentials{}), capabilityInspect, inspectInputJSON("inst-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := output["region_id"]; ok {
		t.Fatalf("oversized region kept: %v", output)
	}
	if _, ok := output["plan_id"]; ok {
		t.Fatalf("oversized plan kept: %v", output)
	}
}

func TestInspectRefusals(t *testing.T) {
	other := func(adapterID, env, kind string) string {
		return `{"instance_ref":` + instanceRefJSON(adapterID, env, kind, "inst-1") + `}`
	}
	cases := map[string]struct {
		reader      *fakeReader
		credentials CredentialResolver
		input, want string
	}{
		"other adapter":      {&fakeReader{}, fakeCredentials{}, other("hosting.other", testEnv, "instance"), "instance reference is not bound to this Vultr adapter and environment"},
		"other environment":  {&fakeReader{}, fakeCredentials{}, other(testAdapterID, "production", "instance"), "instance reference is not bound to this Vultr adapter and environment"},
		"other kind":         {&fakeReader{}, fakeCredentials{}, other(testAdapterID, testEnv, "volume"), "instance reference is not bound to this Vultr adapter and environment"},
		"request failed":     {&fakeReader{err: vultrapi.ErrRequestFailed}, fakeCredentials{}, inspectInputJSON("inst-1"), "Vultr instance inspection failed; the instance state is unknown"},
		"credential failed":  {&fakeReader{}, fakeCredentials{err: errors.New("locked")}, inspectInputJSON("inst-1"), "Vultr instance inspection failed; the instance state is unknown"},
		"different instance": {&fakeReader{instance: vultrapi.Instance{ID: "inst-2", Status: "active"}}, fakeCredentials{}, inspectInputJSON("inst-1"), "Vultr returned a different instance than the one requested"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := invoke(t, testRegistry(t, test.reader, test.credentials), capabilityInspect, test.input)
			if !strings.Contains(errorText(err), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestInspectRejectsMalformedInputBeforeTheProvider(t *testing.T) {
	vultr, err := newAdapter(Config{AdapterID: testAdapterID, EnvironmentID: testEnv, APITokenRef: testTokenRef}, "0.21.0-draft", fakeCredentials{}, &fakeReader{})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{`, `{"instance_ref":{},"extra":1}`, inspectInputJSON("inst-1") + `{}`} {
		if _, err := vultr.inspect(t.Context(), []byte(input)); errorText(err) != "invalid Vultr instance inspect input" {
			t.Fatalf("%s: err = %v", input, err)
		}
	}
}
