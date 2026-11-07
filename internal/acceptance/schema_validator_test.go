package acceptance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func newSchemaValidator(t *testing.T) *SchemaRequestValidator {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	validator, err := NewSchemaRequestValidator(schemas)
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func validDefineRequest() store.OperationRecord {
	return store.OperationRecord{
		OperationType:   "server.define",
		ProtocolVersion: "0.21.0-draft",
		Target:          store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{
			OperationType: "server.define",
			Parameters:    json.RawMessage(`{"server_id":"server-1","environment_id":"staging","display_name":"Test server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`),
		},
	}
}

func TestSchemaRequestValidatorAcceptsAndBindsStandardRequest(t *testing.T) {
	validator := newSchemaValidator(t)
	if err := validator.ValidateOperationRequest(context.Background(), validDefineRequest()); err != nil {
		t.Fatalf("valid server.define request: %v", err)
	}
	request := validDefineRequest()
	request.Deadline = ptrTime(t, "2026-11-07T10:30:00Z")
	if err := validator.ValidateOperationRequest(context.Background(), request); err != nil {
		t.Fatalf("valid request deadline: %v", err)
	}
	provision := store.OperationRecord{
		OperationType: "server.provision", ProtocolVersion: "0.21.0-draft",
		Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{
			OperationType: "server.provision",
			Parameters:    json.RawMessage(`{"server_id":"server-1","environment_id":"staging","hosting_adapter_id":"hosting.example","region_id":"sea","plan_id":"vc2-1c-1gb","os":{"provider_os_id":"1743","distribution":"Debian","version":"12","architecture":"x86_64"},"bootstrap":{"source_uri":"https://example.invalid/bootstrap","revision":"v1","digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"game":{"adapter_id":"game.paper","release_ref":"paper:1.21.4"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`),
		},
	}
	if err := validator.ValidateOperationRequest(context.Background(), provision); err != nil {
		t.Fatalf("valid server.provision request: %v", err)
	}
}

func TestSchemaRequestValidatorRejectsInvalidProtocolRequest(t *testing.T) {
	validator := newSchemaValidator(t)
	tests := []struct {
		name   string
		mutate func(*store.OperationRecord)
	}{
		{name: "protocol version", mutate: func(r *store.OperationRecord) { r.ProtocolVersion = "0.20.0-draft" }},
		{name: "operation mismatch", mutate: func(r *store.OperationRecord) { r.RequestSnapshot.OperationType = "server.start" }},
		{name: "target mismatch", mutate: func(r *store.OperationRecord) { r.Target.ServerID = "other-server" }},
		{name: "unknown property", mutate: func(r *store.OperationRecord) {
			r.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":"server-1","environment_id":"staging","display_name":"Test server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","unexpected":true,"game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`)
		}},
		{name: "duplicate property", mutate: func(r *store.OperationRecord) {
			r.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":"server-1","server_id":"server-1","environment_id":"staging","display_name":"Test server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`)
		}},
		{name: "unknown operation", mutate: func(r *store.OperationRecord) {
			r.OperationType = "server.fake"
			r.RequestSnapshot.OperationType = "server.fake"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validDefineRequest()
			test.mutate(&request)
			if err := validator.ValidateOperationRequest(context.Background(), request); err == nil {
				t.Fatal("invalid request was accepted")
			}
		})
	}
}

func TestSchemaRequestValidatorRejectsMalformedSecretReferenceAndCancellation(t *testing.T) {
	validator := newSchemaValidator(t)
	request := validDefineRequest()
	request.RequestSnapshot.SecretRefs = []store.SecretReference{{SecretRef: "not-a-reference", Purpose: "staging storage"}}
	if err := validator.ValidateOperationRequest(context.Background(), request); err == nil {
		t.Fatal("malformed secret reference was accepted")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validator.ValidateOperationRequest(ctx, validDefineRequest()); err == nil {
		t.Fatal("cancelled validation was accepted")
	}
}

func ptrTime(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return &parsed
}
