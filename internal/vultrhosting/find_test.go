package vultrhosting

import (
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

func findInputJSON(key string) string { return `{"resource_key":"` + key + `"}` }

func matchIDs(t *testing.T, output map[string]any) []string {
	t.Helper()
	matches, ok := output["matches"].([]any)
	if !ok || output["inventory_complete"] != true || output["resource_key"] != testKey {
		t.Fatalf("output = %v", output)
	}
	ids := []string{}
	for _, match := range matches {
		ref := match.(map[string]any)["instance_ref"].(map[string]any)
		if ref["adapter_id"] != testAdapterID || ref["environment_id"] != testEnv || ref["resource_kind"] != "instance" {
			t.Fatalf("match ref = %v", ref)
		}
		ids = append(ids, ref["external_id"].(string))
	}
	return ids
}

func TestFindByResourceKeyReturnsExactDistinctMatchesUpToTwo(t *testing.T) {
	running := func(id, label string) vultrapi.Instance {
		return vultrapi.Instance{ID: id, Label: label, Status: "active", PowerStatus: "running", ServerStatus: "ok"}
	}
	cases := map[string]struct {
		instances []vultrapi.Instance
		want      string
	}{
		"none":            {nil, ""},
		"one":             {[]vultrapi.Instance{running("a", testKey)}, "a"},
		"other labels":    {[]vultrapi.Instance{running("a", testKey+"x"), running("b", "gsc-managed-op-1")}, ""},
		"duplicate entry": {[]vultrapi.Instance{running("a", testKey), running("a", testKey)}, "a"},
		"missing ID":      {[]vultrapi.Instance{running("", testKey)}, ""},
		"capped at two":   {[]vultrapi.Instance{running("a", testKey), running("b", testKey), running("c", testKey)}, "a,b"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{instances: test.instances}
			output, err := invoke(t, testRegistry(t, reader, fakeCredentials{}), capabilityFind, findInputJSON(testKey))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(matchIDs(t, output), ","); got != test.want {
				t.Fatalf("matches = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFindByResourceKeyReportsMatchState(t *testing.T) {
	reader := &fakeReader{instances: []vultrapi.Instance{{ID: "a", Label: testKey, Status: "pending"}}}
	output, err := invoke(t, testRegistry(t, reader, fakeCredentials{}), capabilityFind, findInputJSON(testKey))
	if err != nil {
		t.Fatal(err)
	}
	if state := output["matches"].([]any)[0].(map[string]any)["state"]; state != "creating" {
		t.Fatalf("state = %v", state)
	}
}

func TestFindByResourceKeyRefusals(t *testing.T) {
	vultr, err := newAdapter(Config{AdapterID: testAdapterID, EnvironmentID: testEnv, APITokenRef: testTokenRef}, "0.21.0-draft", fakeCredentials{}, &fakeReader{})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{`, findInputJSON("gsc-managed-1"), `{"resource_key":"` + testKey + `","extra":true}`} {
		if _, err := vultr.findByResourceKey(t.Context(), []byte(input)); errorText(err) != "invalid Vultr resource key lookup input" {
			t.Fatalf("%s: err = %v", input, err)
		}
	}
	failing := &fakeReader{err: vultrapi.ErrInvalidResponse}
	_, err = invoke(t, testRegistry(t, failing, fakeCredentials{}), capabilityFind, findInputJSON(testKey))
	if !strings.Contains(errorText(err), "Vultr inventory lookup failed; matches are unknown") {
		t.Fatalf("err = %v", err)
	}
}
