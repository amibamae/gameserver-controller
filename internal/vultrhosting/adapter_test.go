package vultrhosting

import (
	"context"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

func TestNewRequiresCompleteConfiguration(t *testing.T) {
	valid := Config{AdapterID: testAdapterID, EnvironmentID: testEnv, APITokenRef: testTokenRef}
	cases := map[string]struct {
		config      Config
		version     string
		credentials CredentialResolver
		want        string
	}{
		"no adapter ID":  {Config{EnvironmentID: testEnv, APITokenRef: testTokenRef}, "v", fakeCredentials{}, "Vultr hosting adapter needs an adapter ID, environment ID, and API token reference"},
		"no environment": {Config{AdapterID: testAdapterID, APITokenRef: testTokenRef}, "v", fakeCredentials{}, "Vultr hosting adapter needs an adapter ID, environment ID, and API token reference"},
		"no token ref":   {Config{AdapterID: testAdapterID, EnvironmentID: testEnv}, "v", fakeCredentials{}, "Vultr hosting adapter needs an adapter ID, environment ID, and API token reference"},
		"no version":     {valid, "", fakeCredentials{}, "Vultr hosting adapter needs a protocol version and a credential resolver"},
		"no credentials": {valid, "v", nil, "Vultr hosting adapter needs a protocol version and a credential resolver"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(test.config, test.version, test.credentials, nil); errorText(err) != test.want {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
	if _, err := New(valid, "v", fakeCredentials{}, nil); err != nil {
		t.Fatalf("valid config: %v", err)
	}
}

func TestDescriptorIsReadOnlyHosting(t *testing.T) {
	registry := testRegistry(t, &fakeReader{}, fakeCredentials{})
	descriptor, err := registry.Descriptor(testAdapterID)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Category != adapter.CategoryHosting || len(descriptor.Capabilities) != 2 {
		t.Fatalf("descriptor = %+v", descriptor)
	}
	for _, capability := range descriptor.Capabilities {
		if capability.Mutating {
			t.Fatalf("%s is mutating", capability.Name)
		}
	}
}

func TestInvokeRefusals(t *testing.T) {
	registry := testRegistry(t, &fakeReader{}, fakeCredentials{})
	vultr, err := newAdapter(Config{AdapterID: testAdapterID, EnvironmentID: testEnv, APITokenRef: testTokenRef}, "0.21.0-draft", fakeCredentials{}, &fakeReader{})
	if err != nil {
		t.Fatal(err)
	}
	var nilContext context.Context
	if _, err := vultr.Invoke(nilContext, testRequest(capabilityInspect, inspectInputJSON("a"))); errorText(err) != "Vultr hosting adapter and invocation context are required" {
		t.Fatalf("nil context: %v", err)
	}
	unauthorized := testRequest(capabilityInspect, inspectInputJSON("a"))
	unauthorized.Params.RequestContext.SecretRefs = nil
	if _, err := registry.Invoke(context.Background(), testAdapterID, unauthorized); !strings.Contains(errorText(err), "the Vultr API token reference is not authorized for this invocation") {
		t.Fatalf("unauthorized: %v", err)
	}
	if _, err := vultr.Invoke(context.Background(), testRequest("hosting.instance.destroy", `{}`)); errorText(err) != "unsupported Vultr hosting capability" {
		t.Fatalf("unsupported: %v", err)
	}
}
