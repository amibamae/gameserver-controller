package wasabistorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type stagingWriteFixture struct {
	Capability string `json:"contract"`
	Actor      string `json:"actor_environment"`
	Target     string `json:"target_environment"`
	Expected   struct {
		Accepted            bool `json:"accepted"`
		SideEffectPermitted bool `json:"side_effect_permitted"`
	} `json:"expected"`
}

func readStagingWriteFixture(t *testing.T) stagingWriteFixture {
	t.Helper()
	path := filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "staging-production-write-scope-denied.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture stagingWriteFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Expected.Accepted || fixture.Expected.SideEffectPermitted {
		t.Fatal("fixture must expect denial without a side effect")
	}
	return fixture
}

func TestStagingWriteConformanceDeniesProductionTarget(t *testing.T) {
	fixture := readStagingWriteFixture(t)
	config := testConfig()
	config.EnvironmentID = fixture.Actor
	provider := newProvider(nil)
	clientOpens := 0
	impl, err := newAdapter(config, testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client {
		clientOpens++
		return provider
	})
	if err != nil {
		t.Fatal(err)
	}

	content := []byte("fixture backup")
	digest := sha256.Sum256(content)
	target := testRef("server/world.tar", "").objectTarget
	input := putInput{Target: target, ExpectedSizeBytes: int64(len(content)),
		ExpectedChecksum: checksum{Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}}
	invokeForTarget := func(environment string) error {
		input.Target.EnvironmentID = environment
		request := requestFor(fixture.Capability, input, time.Now().Add(time.Minute), "controller_to_adapter", int64(len(content)))
		request.Params.RequestContext.EnvironmentID = fixture.Actor
		request.Params.RequestContext.SetStreamSources(map[string]io.ReadCloser{
			testTransferID(): io.NopCloser(bytes.NewReader(content)),
		})
		_, err := impl.Invoke(context.Background(), request)
		return err
	}

	if err := invokeForTarget(fixture.Target); err == nil || err.Error() != "invalid object upload request" {
		t.Fatalf("cross-environment write: got %v, want target scope rejection", err)
	}
	if clientOpens != 0 || len(provider.objects) != 0 || len(provider.uploads) != 0 {
		t.Fatalf("denied write reached provider: client opens = %d, objects = %d, uploads = %d", clientOpens, len(provider.objects), len(provider.uploads))
	}
	if err := invokeForTarget(fixture.Actor); err != nil {
		t.Fatalf("same-environment control: %v", err)
	}
	if clientOpens != 1 || !bytes.Equal(provider.objects[target.Container+"/"+target.Key], content) {
		t.Fatalf("control did not write the expected bytes: client opens = %d, objects = %d", clientOpens, len(provider.objects))
	}
}
