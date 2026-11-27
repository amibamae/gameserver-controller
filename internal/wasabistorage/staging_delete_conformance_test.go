package wasabistorage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStagingDeleteConformanceDeniesProductionTarget(t *testing.T) {
	path := filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "staging-production-delete-scope-denied.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Capability string `json:"contract_area"`
		Actor      string `json:"actor_environment"`
		Target     string `json:"target_environment"`
		Expected   struct {
			Accepted    bool `json:"accepted"`
			DeleteCalls int  `json:"adapter_delete_calls"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}

	config := testConfig()
	config.EnvironmentID = fixture.Actor
	const key = "server/world.tar"
	provider := newProvider(map[string]string{key: "fixture backup"})
	clientOpens := 0
	impl, err := newAdapter(config, testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client {
		clientOpens++
		return provider
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := testRef(key, "version-1")
	ref.EnvironmentID = fixture.Target
	deadline := time.Now().Add(time.Minute)
	request := requestFor(fixture.Capability, DeleteInput{ObjectRef: ref}, deadline, "", 0)
	request.Params.RequestContext.EnvironmentID = fixture.Actor
	ctx := context.Background()
	_, err = impl.Invoke(ctx, request)
	if err == nil || err.Error() != "invalid object delete request" {
		t.Fatalf("cross-environment delete: got %v, want target scope rejection", err)
	}
	if fixture.Expected.Accepted || fixture.Expected.DeleteCalls != 0 {
		t.Fatal("fixture must expect a denied delete and zero provider deletes")
	}
	if clientOpens != 0 || provider.deletes != fixture.Expected.DeleteCalls {
		t.Fatalf("denied request reached provider: client opens = %d, deletes = %d", clientOpens, provider.deletes)
	}

	// The same version can be deleted inside the actor's environment.
	ref.EnvironmentID = fixture.Actor
	control := requestFor(fixture.Capability, DeleteInput{ObjectRef: ref}, deadline, "", 0)
	control.Params.RequestContext.EnvironmentID = fixture.Actor
	if _, err := impl.Invoke(ctx, control); err != nil {
		t.Fatalf("same-environment control: %v", err)
	}
	if clientOpens != 1 || provider.deletes != 1 {
		t.Fatalf("control did not delete through provider: client opens = %d, deletes = %d", clientOpens, provider.deletes)
	}
}
