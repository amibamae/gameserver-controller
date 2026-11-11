package adapterprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

const (
	childModeEnv  = "GSC_ADAPTERPROCESS_CHILD"
	refSchema     = "urn:gameserver-controller:0.21.0-draft:common:external-resource-ref"
	capabilityRef = "hosting.fake.inspect"
	instanceRef   = `{"adapter_id":"hosting.fake","environment_id":"test","resource_kind":"instance","external_id":"vm-1"}`
)

func loadSchemas(t testing.TB) *schemaregistry.Registry {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}

func fakeDescriptor(version string, capabilities ...string) adapter.Descriptor {
	descriptor := adapter.Descriptor{AdapterID: "hosting.fake", Category: adapter.CategoryHosting, DisplayName: "process fake",
		ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{}}
	for _, name := range capabilities {
		descriptor.Capabilities = append(descriptor.Capabilities, adapter.Capability{Name: name, Version: "1", Mutating: true,
			InputSchemaURI: refSchema, OutputSchemaURI: refSchema, RequiredPermissions: []string{}})
	}
	return descriptor
}

func scriptedFake(t testing.TB, version string, delay time.Duration) *fakeadapter.Scripted {
	fake := fakeadapter.New(fakeDescriptor(version, capabilityRef))
	if err := fake.SetScript(capabilityRef, fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(instanceRef),
		ObservedAt: time.Now().UTC(), Delay: delay}); err != nil {
		t.Fatal(err)
	}
	return fake
}

func invokeRequest(version string, deadline time.Time) adapter.InvokeRequest {
	return adapter.InvokeRequest{JSONRPC: "2.0", ID: "request-1", Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: version, Capability: capabilityRef, Input: json.RawMessage(instanceRef),
		RequestContext: adapter.RequestContext{RequestID: "request-1", CorrelationID: "correlation-1", OperationID: "operation-1",
			StepID: "step-1", EnvironmentID: "test", Deadline: deadline}}}
}

// crashing exits mid-call, as a crashed adapter would.
type crashing struct{ *fakeadapter.Scripted }

func (crashing) Invoke(context.Context, adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	os.Exit(3)
	return adapter.InvokeResponse{}, nil
}

// hung ignores its deadline, so only the controller can detect the stall.
type hung struct{ *fakeadapter.Scripted }

func (hung) Invoke(context.Context, adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	select {}
}

// noisy writes debug text to stdout, which violates the binding.
type noisy struct{ *fakeadapter.Scripted }

func (n noisy) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	fmt.Println("debug: invoking")
	return n.Scripted.Invoke(ctx, request)
}

// TestHelperAdapterProcess is not a real test. The process tests re-run the
// test binary with childModeEnv set, and this function then acts as the adapter.
func TestHelperAdapterProcess(t *testing.T) {
	mode := os.Getenv(childModeEnv)
	if mode == "" {
		return
	}
	schemas := loadSchemas(t)
	version := schemas.ProtocolVersion()
	var impl adapter.Implementation = scriptedFake(t, version, 0)
	switch mode {
	case "crash":
		impl = crashing{scriptedFake(t, version, 0)}
	case "stray":
		impl = noisy{scriptedFake(t, version, 0)}
	case "hang":
		impl = hung{scriptedFake(t, version, 0)}
	case "storage":
		impl = fakeadapter.New(adapter.Descriptor{AdapterID: "storage.fake", Category: adapter.CategoryStorage, DisplayName: "storage fake",
			ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
			Capabilities: []adapter.Capability{{Name: "storage.object.put", Version: "1", Mutating: true,
				InputSchemaURI: "urn:gameserver-controller:" + version + ":storage:object-put-input", OutputSchemaURI: "urn:gameserver-controller:" + version + ":storage:object-put-output", RequiredPermissions: []string{}}}})
	}
	if err := Serve(context.Background(), impl, os.Stdin, os.Stdout, ServeOptions{Schemas: schemas}); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(2)
	}
	os.Exit(0)
}

func startChild(t *testing.T, schemas *schemaregistry.Registry, mode string) (*Process, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Start(context.Background(), schemas, Config{Path: executable, Args: []string{"-test.run=^TestHelperAdapterProcess$"},
		Env: []string{childModeEnv + "=" + mode}, ControllerID: "test.controller", ControllerVersion: "0.0.1", StopTimeout: 5 * time.Second})
}

func TestProcessRegistersAndInvokesThroughRegistry(t *testing.T) {
	schemas := loadSchemas(t)
	process, err := startChild(t, schemas, "ok")
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(process); err != nil {
		t.Fatal(err)
	}
	if status, err := process.Health(context.Background()); err != nil || status != "healthy" {
		t.Fatalf("health = %q, %v", status, err)
	}
	response, err := registry.Invoke(context.Background(), "hosting.fake", invokeRequest(schemas.ProtocolVersion(), time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if response.Result.Outcome != adapter.OutcomeCompleted || !bytes.Contains(response.Result.Output, []byte(`"vm-1"`)) {
		t.Fatalf("unexpected response: %+v", response)
	}
	if err := process.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := process.Invoke(context.Background(), invokeRequest(schemas.ProtocolVersion(), time.Now().Add(time.Minute))); !errors.Is(err, ErrProcessFailed) {
		t.Fatalf("invoke after close = %v", err)
	}
}

func TestProcessFailuresAreUncertainForMutatingCalls(t *testing.T) {
	schemas := loadSchemas(t)
	for _, test := range []struct {
		mode     string
		deadline time.Duration
	}{{"crash", time.Minute}, {"stray", time.Minute}, {"hang", 2 * time.Second}} {
		t.Run(test.mode, func(t *testing.T) {
			process, err := startChild(t, schemas, test.mode)
			if err != nil {
				t.Fatal(err)
			}
			defer process.Close()
			registry, err := adapter.NewRegistry(schemas)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(process); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			_, err = registry.Invoke(context.Background(), "hosting.fake", invokeRequest(schemas.ProtocolVersion(), time.Now().Add(test.deadline)))
			var invocation *adapter.InvocationError
			if !errors.As(err, &invocation) || !invocation.MayHaveSideEffects || !errors.Is(err, ErrProcessFailed) {
				t.Fatalf("want an uncertain process failure, got %v", err)
			}
			if test.mode != "hang" && time.Since(started) > 30*time.Second {
				t.Fatal("failure was only detected by the deadline")
			}
		})
	}
}

func TestProcessRejectsStreamCapabilitiesWithoutStreamProfile(t *testing.T) {
	if _, err := startChild(t, loadSchemas(t), "storage"); err == nil || !strings.Contains(err.Error(), "local-unix-stream-v1") {
		t.Fatalf("want stream-profile rejection, got %v", err)
	}
}

func TestStartRequiresAbsolutePath(t *testing.T) {
	if _, err := Start(context.Background(), loadSchemas(t), Config{Path: "adapter", ControllerID: "c", ControllerVersion: "1"}); err == nil {
		t.Fatal("accepted a relative executable path")
	}
}

// serveLines runs Serve over the given input lines and returns its response frames.
func serveLines(t *testing.T, impl adapter.Implementation, lines ...string) []envelope {
	t.Helper()
	var output bytes.Buffer
	if err := Serve(context.Background(), impl, strings.NewReader(strings.Join(lines, "\n")+"\n"), &output, ServeOptions{Schemas: loadSchemas(t)}); err != nil {
		t.Fatal(err)
	}
	var responses []envelope
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		var message envelope
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		responses = append(responses, message)
	}
	return responses
}

func initializeLine(version string) string {
	return `{"jsonrpc":"2.0","id":"init","method":"adapter.initialize","params":{"protocol_versions":["` + version + `"],"controller":{"implementation_id":"test","implementation_version":"1"}}}`
}

func TestServeRejectsInvalidFramesAndRequiresInitialize(t *testing.T) {
	schemas := loadSchemas(t)
	version := schemas.ProtocolVersion()
	responses := serveLines(t, scriptedFake(t, version, 0),
		`{"jsonrpc":"2.0","id":"early","method":"adapter.describe","params":{}}`,
		`not json`,
		`[{"jsonrpc":"2.0","id":"b","method":"adapter.describe"}]`,
		`{"jsonrpc":"2.0","method":"adapter.describe"}`,
		`{"jsonrpc":"2.0","id":7,"method":"adapter.describe"}`,
		`{"jsonrpc":"2.0","id":"dup","id":"dup2","method":"adapter.describe"}`,
		"",
		initializeLine(version),
		`{"jsonrpc":"2.0","id":"again","method":"adapter.initialize","params":{"protocol_versions":["`+version+`"],"controller":{"implementation_id":"test","implementation_version":"1"}}}`,
		`{"jsonrpc":"2.0","id":"unknown","method":"adapter.reconcile","params":{}}`,
		"{\"jsonrpc\":\"2.0\",\"id\":\"crlf\",\"method\":\"adapter.health\",\"params\":{}}\r",
	)
	want := []int{codeInvalidRequest, codeParseError, codeInvalidRequest, codeInvalidRequest, codeInvalidRequest, codeParseError, codeParseError, 0, codeInvalidRequest, codeMethodNotFound, 0}
	if len(responses) != len(want) {
		t.Fatalf("got %d responses, want %d: %+v", len(responses), len(want), responses)
	}
	for i, code := range want {
		got := 0
		if responses[i].Error != nil {
			got = responses[i].Error.Code
		}
		if got != code {
			t.Fatalf("response %d: code %d, want %d (%+v)", i, got, code, responses[i])
		}
	}
	if string(responses[10].ID) != `"crlf"` {
		t.Fatalf("CRLF frame was not answered with its ID: %s", responses[10].ID)
	}
}

func TestServeRejectsOversizedAndInvalidUTF8Frames(t *testing.T) {
	version := loadSchemas(t).ProtocolVersion()
	oversized := `{"jsonrpc":"2.0","id":"big","method":"adapter.health","params":{"x":"` + strings.Repeat("a", MaxFrameBytes) + `"}}`
	responses := serveLines(t, scriptedFake(t, version, 0), initializeLine(version), oversized, "{\"jsonrpc\":\"2.0\",\"id\":\"\xff\"}",
		`{"jsonrpc":"2.0","id":"after","method":"adapter.health","params":{}}`)
	if len(responses) != 4 || responses[1].Error == nil || responses[2].Error == nil || responses[3].Error != nil {
		t.Fatalf("unexpected responses: %+v", responses)
	}
}

func TestServeNegotiatesVersionAndRejectsStreamsAndMismatchedVersions(t *testing.T) {
	version := loadSchemas(t).ProtocolVersion()
	request := invokeRequest(version, time.Now().Add(time.Minute))
	ok, _ := json.Marshal(request)
	wrongVersion := request
	wrongVersion.ID, wrongVersion.Params.ProtocolVersion = "wrong", "0.0.1-draft"
	badVersion, _ := json.Marshal(wrongVersion)
	responses := serveLines(t, scriptedFake(t, version, 0),
		`{"jsonrpc":"2.0","id":"init0","method":"adapter.initialize","params":{"protocol_versions":["0.0.1-draft"],"controller":{"implementation_id":"test","implementation_version":"1"}}}`,
		initializeLine(version), string(ok), string(badVersion))
	if responses[0].Error == nil || !bytes.Contains(responses[0].Error.Data, []byte("adapterprocess.unsupported_protocol_version")) {
		t.Fatalf("unsupported version not rejected: %+v", responses[0])
	}
	if responses[1].Error != nil || responses[2].Error != nil || !bytes.Contains(responses[2].Result, []byte("vm-1")) {
		t.Fatalf("invoke failed: %+v", responses[1:3])
	}
	if responses[3].Error == nil {
		t.Fatal("accepted an invoke with a different protocol version")
	}
}

func TestFrameReaderBoundaries(t *testing.T) {
	reader := newFrameReader(io.MultiReader(strings.NewReader(`{"a":`), strings.NewReader("1}\n{\"b\":2}")))
	frame, err := reader.next()
	if err != nil || string(frame) != `{"a":1}` {
		t.Fatalf("split frame = %q, %v", frame, err)
	}
	if _, err := reader.next(); !errors.Is(err, errTruncatedFrame) {
		t.Fatalf("unterminated final frame = %v", err)
	}
	if _, err := newFrameReader(strings.NewReader("")).next(); !errors.Is(err, io.EOF) {
		t.Fatalf("empty input = %v", err)
	}
}
