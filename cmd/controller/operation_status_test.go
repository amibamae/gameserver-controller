package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func statusFixture(t *testing.T) (string, store.OperationRecord) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	record := store.OperationRecord{
		OperationID: "op-42", OperationType: "test.inspect", ProtocolVersion: "0.21.0-draft",
		Target:  store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		ActorID: "fixture-actor", IdempotencyKey: "fixture-request",
		RequestFingerprint: "sha256:" + strings.Repeat("a", 64),
		RequestSnapshot:    store.RequestSnapshot{OperationType: "test.inspect", Parameters: json.RawMessage(`{}`)},
	}
	proof := store.IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []store.IdempotencyVerifier{
		{KeyVersion: "v1", Digest: make([]byte, 32), BindingDigest: bytes.Repeat([]byte{1}, 32)},
	}}
	stored, _, err := db.AcceptOperation(record, proof, []string{"fixture-lock"})
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	return path, stored.Record
}

func TestOfflineOperationStatus(t *testing.T) {
	path, record := statusFixture(t)
	args := []string{"operation-status", "--store", path, record.OperationID}
	var out bytes.Buffer
	if err := run(context.Background(), args, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"operation_id": "op-42", "operation_type": "test.inspect", "status": "pending",
		"server_id": "server-1", "environment_id": "staging", "updated_at": record.UpdatedAt.Format(time.RFC3339Nano)}
	if !maps.Equal(got, want) {
		t.Fatalf("summary = %v, want %v", got, want)
	}
	reader, writer := io.Pipe()
	reader.Close()
	defer writer.Close()
	if err := run(context.Background(), args, writer, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("closed output: %v", err)
	}
	args[3] = "absent"
	out.Reset()
	err := run(context.Background(), args, &out, io.Discard)
	if !errors.Is(err, store.ErrNotFound) || out.Len() != 0 {
		t.Fatalf("missing operation: output %q, error %v", out.String(), err)
	}
}

func TestOfflineOperationStatusArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-h"}, ""},
		{[]string{"--invalid"}, "flag provided but not defined: -invalid"},
		{[]string{"op"}, "operation-status requires --store PATH and one operation ID"},
		{[]string{"--store", "db"}, "operation-status requires --store PATH and one operation ID"},
		{[]string{"--store", "db", " "}, "operation-status requires --store PATH and one operation ID"},
		{[]string{"--store", "db", "a", "b"}, "operation-status requires --store PATH and one operation ID"},
	} {
		err := operationStatus(tc.args, io.Discard, io.Discard)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.want {
			t.Errorf("%v: error %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestOfflineOperationStatusMissingStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	err := operationStatus([]string{"--store", path, "op"}, io.Discard, io.Discard)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open missing store: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat after inspection: %v", err)
	}
}
