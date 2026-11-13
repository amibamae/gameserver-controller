package wasabistorage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

// These tests call Invoke directly, below the registry's own request checks,
// to pin the adapter's own context, deadline, and dispatch rules.

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	provider := &memoryS3{objects: map[string][]byte{}, uploads: map[string]memoryUpload{}}
	impl, err := newAdapter(testConfig(), testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	return impl
}

func TestInvokeRejectsBeforeDispatch(t *testing.T) {
	impl := newTestAdapter(t)
	head := headInput{ObjectRef: objectReference{objectTarget: objectTarget{AdapterID: testAdapterID,
		StorageProfile: "staging-storage", EnvironmentID: "staging", Container: "backups", Key: "server/world.tar"}}}
	var missing context.Context
	tests := []struct {
		name    string
		ctx     context.Context
		adapter *Adapter
		request adapter.InvokeRequest
		want    string
	}{
		{"missing context", missing, impl, requestFor("storage.object.head", head, time.Now().Add(time.Minute), "", 0), "storage adapter and invocation context are required"},
		{"missing adapter", context.Background(), nil, requestFor("storage.object.head", head, time.Now().Add(time.Minute), "", 0), "storage adapter and invocation context are required"},
		{"no deadline", context.Background(), impl, requestFor("storage.object.head", head, time.Time{}, "", 0), "a future operation deadline is required"},
		{"deadline already passed", context.Background(), impl, requestFor("storage.object.head", head, time.Now().Add(-time.Second), "", 0), "a future operation deadline is required"},
		{"unsupported capability", context.Background(), impl, requestFor("storage.object.purge", head, time.Now().Add(time.Minute), "", 0), "unsupported Wasabi storage capability"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := test.adapter.Invoke(test.ctx, test.request)
			if err == nil || err.Error() != test.want || response.Result.Output != nil {
				t.Fatalf("Invoke = %+v, %v; want %q", response, err, test.want)
			}
		})
	}
}

func TestInvokeDispatchesEveryAdvertisedCapability(t *testing.T) {
	impl := newTestAdapter(t)
	capabilities := impl.Descriptor().Capabilities
	if len(capabilities) != 8 {
		t.Fatalf("descriptor advertises %d capabilities, want the eight Invoke dispatches", len(capabilities))
	}
	for _, capability := range capabilities {
		t.Run(capability.Name, func(t *testing.T) {
			// An empty input reaches the capability's own handler, which
			// rejects it; only an undispatched name reports unsupported.
			request := requestFor(capability.Name, map[string]any{}, time.Now().Add(time.Minute), "", 0)
			request.Params.Input = json.RawMessage(`{}`)
			_, err := impl.Invoke(context.Background(), request)
			if err == nil || err.Error() == "unsupported Wasabi storage capability" {
				t.Fatalf("Invoke(%s) = %v, want the handler's own rejection", capability.Name, err)
			}
		})
	}
}
