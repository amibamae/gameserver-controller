package workflow

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBackupTransferReferencesStayInTransientRequestContext(t *testing.T) {
	transferID := strings.Repeat("e", 64)
	stableInput := json.RawMessage(`{"expected_size_bytes":4,"expected_checksum":{"algorithm":"sha256","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
	operation := store.OperationRecord{
		OperationID: "backup-stream-test", OperationType: "server.backup",
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.backup", Parameters: stableInput},
		Steps:           []store.OperationStep{{StepID: "upload-world", Input: stableInput, ResolvedInput: stableInput}},
	}
	encodedOperation, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedOperation), transferID) {
		t.Fatal("durable backup operation contains a bearer transfer ID")
	}
	request := adapter.InvokeRequest{Params: adapter.InvokeParams{RequestContext: adapter.RequestContext{
		StreamTransfers: []capturestream.Reference{{
			TransferID: transferID, Direction: "controller_to_adapter", MaximumBytes: 4,
			ExpiresAt: time.Now().UTC().Add(time.Minute),
		}},
	}}}
	if strings.Contains(fmt.Sprintf("%+v", request), transferID) || strings.Contains(fmt.Sprintf("%#v", request), transferID) {
		t.Fatal("ordinary request formatting exposed an invocation transfer ID")
	}
	wire, err := json.Marshal(request)
	if err != nil || !strings.Contains(string(wire), transferID) {
		t.Fatalf("transient request context must carry the transfer ID: %s, err=%v", wire, err)
	}
}

func TestRequestContextStreamsAreOneUseAndNotSerialized(t *testing.T) {
	transferID := strings.Repeat("f", 64)
	requestContext := adapter.RequestContext{StreamTransfers: []capturestream.Reference{{
		TransferID: transferID, Direction: "controller_to_adapter", MaximumBytes: 4,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}}}
	requestContext.SetStreamSources(map[string]io.ReadCloser{transferID: io.NopCloser(strings.NewReader("data"))})
	reader, err := requestContext.OpenStream(transferID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestContext.OpenStream(transferID); err == nil {
		t.Fatal("source stream was claimed twice")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "streamAccess") || strings.Contains(string(encoded), "sources") {
		t.Fatalf("invocation-only stream source was serialized: %s", encoded)
	}
	requestContext.ClearStreamSources()
	if _, err := requestContext.OpenStream(transferID); err == nil {
		t.Fatal("cleared stream source remained available")
	}
}
