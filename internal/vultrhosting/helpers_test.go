package vultrhosting

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

const (
	testAdapterID = "hosting.vultr-staging"
	testEnv       = "staging"
	testTokenRef  = "systemd-credential://vultr-api-token"
	testKey       = "gsc-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var testClock = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

// fakeReader stands in for the Vultr client and records the token it saw.
type fakeReader struct {
	instance  vultrapi.Instance
	instances []vultrapi.Instance
	err       error
	token     string
	calls     int
}

func (f *fakeReader) GetInstance(_ context.Context, token []byte, _ string) (vultrapi.Instance, error) {
	f.calls++
	f.token = string(token)
	return f.instance, f.err
}

func (f *fakeReader) ListInstances(_ context.Context, token []byte) ([]vultrapi.Instance, error) {
	f.calls++
	f.token = string(token)
	return f.instances, f.err
}

type fakeCredentials struct{ err error }

func (f fakeCredentials) WithCredential(reference string, use func([]byte) error) error {
	if f.err != nil {
		return f.err
	}
	if reference != testTokenRef {
		return errors.New("unexpected credential reference " + reference)
	}
	return use([]byte("test-token"))
}

// testRegistry registers a Vultr adapter backed by reader in the
// schema-validating registry, so every output is checked against the
// protocol schemas.
func testRegistry(t *testing.T, reader *fakeReader, credentials CredentialResolver) *adapter.Registry {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	vultr, err := newAdapter(Config{AdapterID: testAdapterID, EnvironmentID: testEnv, APITokenRef: testTokenRef}, schemas.ProtocolVersion(), credentials, reader)
	if err != nil {
		t.Fatal(err)
	}
	vultr.now = func() time.Time { return testClock }
	if err := registry.Register(vultr); err != nil {
		t.Fatal(err)
	}
	return registry
}

func testRequest(capability, input string) adapter.InvokeRequest {
	requestContext := adapter.RequestContext{
		RequestID: "request-1", CorrelationID: "correlation-1", EnvironmentID: testEnv,
		Deadline:   time.Now().Add(time.Minute).UTC(),
		SecretRefs: []adapter.SecretReference{{SecretRef: testTokenRef, Purpose: "Vultr read access"}},
	}
	return adapter.InvokeRequest{
		JSONRPC: "2.0", ID: requestContext.RequestID, Method: "adapter.invoke",
		Params: adapter.InvokeParams{
			ProtocolVersion: "0.21.0-draft", Capability: capability,
			RequestContext: requestContext, Input: json.RawMessage(input),
		},
	}
}

// invoke runs a capability through the registry and returns the decoded
// output, or the invocation error.
func invoke(t *testing.T, registry *adapter.Registry, capability, input string) (map[string]any, error) {
	t.Helper()
	response, err := registry.Invoke(context.Background(), testAdapterID, testRequest(capability, input))
	if err != nil {
		return nil, err
	}
	var output map[string]any
	if err := json.Unmarshal(response.Result.Output, &output); err != nil {
		t.Fatal(err)
	}
	return output, nil
}

func instanceRefJSON(adapterID, env, kind, id string) string {
	encoded, err := json.Marshal(instanceRef{AdapterID: adapterID, EnvironmentID: env, ResourceKind: kind, ExternalID: id})
	if err != nil {
		panic(err) // A struct of strings always encodes.
	}
	return string(encoded)
}

func inspectInputJSON(id string) string {
	return `{"instance_ref":` + instanceRefJSON(testAdapterID, testEnv, "instance", id) + `}`
}

func errorText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
