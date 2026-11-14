package workflow

import (
	"context"
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

type backupStreamCharacterizationBroker struct {
	ServerBackupTransferBroker
	transfers []adapter.StreamTransfer
}

func (b backupStreamCharacterizationBroker) IssueStepStreamTransfers(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation, string) ([]adapter.StreamTransfer, error) {
	return b.transfers, nil
}

func TestBackupStreamTransferChecksPreserveErrorOrder(t *testing.T) {
	now := time.Now()
	valid := adapter.StreamTransfer{
		Reference: capturestream.Reference{TransferID: "transfer", Direction: "controller_to_adapter", MaximumBytes: 4, ExpiresAt: now.Add(time.Minute)},
		Source:    io.NopCloser(strings.NewReader("data")), Verify: func() error { return nil },
	}
	unsafe := valid
	unsafe.Reference.MaximumBytes = 5
	missingSource := valid
	missingSource.Source = nil
	for _, tc := range []struct {
		name      string
		transfers []adapter.StreamTransfer
		deadline  time.Time
		err       string
	}{
		{"count before deadline", nil, time.Time{}, "stream broker did not issue exactly one transfer for every planned stream"},
		{"deadline before handle checks", []adapter.StreamTransfer{unsafe, unsafe}, time.Time{}, "stream transfer issuance has no live invocation deadline"},
		{"unsafe bounds before duplicate", []adapter.StreamTransfer{valid, unsafe}, now.Add(2 * time.Minute), "stream broker issued a handle with an unsafe direction, bound, or expiry"},
		{"duplicate before missing source", []adapter.StreamTransfer{valid, missingSource}, now.Add(2 * time.Minute), "stream broker reused a transfer ID within one invocation"},
		{"missing source", []adapter.StreamTransfer{missingSource, valid}, now.Add(2 * time.Minute), "stream broker did not bind its handle to exactly one correct-direction stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &ServerBackupWorkflow{schemas: acceptAllTestSchemas{}, transfers: backupStreamCharacterizationBroker{transfers: tc.transfers}}
			step := store.OperationStep{CapabilityName: "game.backup.validate", Input: json.RawMessage(`{"manifest":{"objects":[{"data_set_id":"one","size_bytes":4},{"data_set_id":"two","size_bytes":4}]}}`)}
			transfers, err := w.StreamTransfersForStep(context.Background(), store.OperationRecord{}, step, tc.deadline)
			if err == nil || err.Error() != tc.err || transfers != nil {
				t.Fatalf("got %d transfers and error %v; want %q", len(transfers), err, tc.err)
			}
		})
	}
}
