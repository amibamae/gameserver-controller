package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// TestFaultUploadAcknowledgmentIsNotBackupProof records a complete backup
// and then corrupts one piece of its storage evidence. A storage upload
// acknowledgment alone never proves a backup: completion must also see the
// full byte count, a complete readback whose checksum equals the captured
// checksum, and controller-side verification of every transferred stream.
func TestFaultUploadAcknowledgmentIsNotBackupProof(t *testing.T) {
	const (
		uploadUnbound   = `upload for data set "world" does not bind the exact capture and target`
		readbackUnbound = `storage readback for data set "world" does not match the captured bytes`
	)
	streamFailure := errors.New("received bytes do not match the expected SHA-256")
	tests := []struct {
		name    string
		change  func(*testing.T, *store.OperationRecord)
		streams map[string]error
		want    string
	}{
		{name: "upload acknowledged as partial", change: backupR3Field(backupR3Upload, "upload_state", "partial"), want: uploadUnbound},
		{name: "upload acknowledged with fewer bytes than captured", change: backupR3Field(backupR3Upload, "bytes_received", 31), want: uploadUnbound},
		{name: "readback checksum mismatch", change: backupR3Field(backupR3Readback, "computed_checksum",
			map[string]any{"algorithm": "sha256", "digest": backupR3OtherDigest}), want: readbackUnbound},
		{name: "readback checksum algorithm differs", change: backupR3Field(backupR3Readback, "computed_checksum",
			map[string]any{"algorithm": "md5", "digest": backupTestDigest}), want: readbackUnbound},
		{name: "readback transfer partial", change: backupR3Field(backupR3Readback, "transfer_state", "partial"), want: readbackUnbound},
		{name: "readback returned fewer bytes than captured", change: backupR3Field(backupR3Readback, "bytes_sent", 31), want: readbackUnbound},
		{name: "upload acknowledged but its stream failed controller verification", streams: map[string]error{backupR3Upload: streamFailure},
			want: "controller-side stream verification failed for " + backupR3Upload + ": " + streamFailure.Error()},
		{name: "readback stream failed controller verification", streams: map[string]error{backupR3Readback: streamFailure},
			want: "controller-side stream verification failed for " + backupR3Readback + ": " + streamFailure.Error()},
		{name: "capture stream failed controller verification", streams: map[string]error{backupR3Capture: streamFailure},
			want: "controller-side stream verification failed for " + backupR3Capture + ": " + streamFailure.Error()},
	}
	f := newBackupR3Fixture(t, false)
	if err := f.backupR3Workflow(f.server, backupR3Streams{}).ValidateCompletion(context.Background(), f.complete); err != nil {
		t.Fatalf("complete backup evidence rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := backupR3Copy(f.complete)
			if test.change != nil {
				test.change(t, &operation)
			}
			err := f.backupR3Workflow(f.server, backupR3Streams{fail: test.streams}).ValidateCompletion(context.Background(), operation)
			if got := backupR3ErrText(err); got != test.want {
				t.Errorf("completion error = %q, want %q", got, test.want)
			}
		})
	}
}
