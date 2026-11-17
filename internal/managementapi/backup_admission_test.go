package managementapi

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type backupAdmissionRecorder struct {
	*testController
	targets []store.OperationTarget
}

func (c *backupAdmissionRecorder) Accept(ctx context.Context, principal authkey.Principal, key string, record store.OperationRecord) (store.StoredOperation, bool, error) {
	c.targets = append(c.targets, record.Target)
	return c.testController.Accept(ctx, principal, key, record)
}

func TestBackupAdmissionRejectsScopeFromAnotherEnvironment(t *testing.T) {
	f := newAPIFixture(t)
	f.define(t, "stage", "staging")
	f.define(t, "prod", "production")
	f.controller.registered = []string{"server.backup"}
	f.controller.network = map[string]bool{"stage": true, "prod": true}
	recorder := &backupAdmissionRecorder{testController: f.controller}
	handler, err := NewAPIHandler(Config{Store: f.controller.store, Controller: recorder, Authenticator: f.tokens,
		Audience: "controller-test", ProtocolVersion: f.controller.schemas.ProtocolVersion()})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler

	token := f.as("backup",
		authkey.Scope{Permission: "server.inspect", Environment: "staging", Resource: "stage"},
		authkey.Scope{Permission: "server.inspect", Environment: "production", Resource: "prod"},
		authkey.Scope{Permission: "server.backup", Environment: "staging", Resource: "prod"},
		authkey.Scope{Permission: "server.backup", Environment: "staging", Resource: "stage"},
	)
	const body = `{"operation_type":"server.backup","parameters":{"backup_profile":"wasabi-worlds"}}`
	denied := f.do(http.MethodPost, "/v1/servers/prod/operations", token, "prod-backup", body)
	if denied.Code != http.StatusForbidden || protocolCode(t, denied) != problemDenied.Code {
		t.Fatalf("production backup = %d %s, want authorization denial", denied.Code, denied.Body)
	}
	if len(recorder.targets) != 0 {
		t.Fatalf("denied request reached acceptance: %+v", recorder.targets)
	}

	accepted := f.do(http.MethodPost, "/v1/servers/stage/operations", token, "stage-backup", body)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("staging backup = %d %s, want acceptance", accepted.Code, accepted.Body)
	}
	want := []store.OperationTarget{{ServerID: "stage", EnvironmentID: "staging"}}
	if !reflect.DeepEqual(recorder.targets, want) {
		t.Fatalf("accepted targets = %+v, want %+v", recorder.targets, want)
	}
}
