package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// provisionR3Objects opens backup objects through open, or fails with err.
type provisionR3Objects struct {
	*staticProvisionBackupResolver
	err  error
	none bool
}

func (r provisionR3Objects) OpenServerProvisionBackupObject(ctx context.Context, server store.LogicalServer, profile string, object json.RawMessage) (io.ReadCloser, error) {
	if r.err != nil || r.none {
		return nil, r.err
	}
	return r.staticProvisionBackupResolver.OpenServerProvisionBackupObject(ctx, server, profile, object)
}

// provisionR3StreamSetup is the restore dispatch fixture: compatibility and
// create succeeded and the restore step is running with resolved input.
func provisionR3StreamSetup(t *testing.T) (provisionPlanFixture, *staticProvisionBackupResolver) {
	t.Helper()
	f, resolver := provisionR3RestoreFixture(t)
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	prepareProvisionRestoreDispatch(t, &f, steps)
	return f, resolver
}

// TestServerProvisionR3StreamTransfers pins StreamTransfersForStep's checks
// in order.
func TestServerProvisionR3StreamTransfers(t *testing.T) {
	const unbound = "restore streams require exact compatible backup and installed target bindings"
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	var missing context.Context
	type run struct {
		f        *provisionPlanFixture
		resolver *staticProvisionBackupResolver
		ctx      context.Context
		step     store.OperationStep
		deadline time.Time
	}
	restoreStep := func(change func(*store.OperationStep)) func(*testing.T, *run) {
		return func(_ *testing.T, r *run) { change(&r.step) }
	}
	cases := []struct {
		name   string
		change func(*testing.T, *run)
		want   string
		count  int
	}{
		{name: "nil context", change: func(_ *testing.T, r *run) { r.ctx = missing }, want: "", count: 0},
		{name: "wrong type", change: func(_ *testing.T, r *run) { r.f.operation.OperationType = "server.start" }},
		{name: "other step", change: restoreStep(func(s *store.OperationStep) { s.StepID = provisionInstallStep })},
		{name: "no backup resolver", change: func(_ *testing.T, r *run) { r.f.workflow.backups = nil }},
		{name: "cancelled", change: func(_ *testing.T, r *run) { r.ctx = cancelled }, want: context.Canceled.Error()},
		{name: "input undecodable", change: restoreStep(func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`[`) }),
			want: "persisted restore input has no verified source backup"},
		{name: "input without backup", change: restoreStep(func(s *store.OperationStep) {
			s.ResolvedInput = provisionR3MustEdit(s.ResolvedInput, "source_backup", "backup-123")
		}), want: "persisted restore input has no verified source backup"},
		{name: "server lookup fails", change: func(_ *testing.T, r *run) {
			r.f.workflow.servers = provisionR3Servers{err: errors.New("store offline")}
		},
			want: "store offline"},
		{name: "profile fails", change: func(_ *testing.T, r *run) {
			r.f.workflow.profiles = provisionR3ProfileResolver{err: errors.New("catalog offline")}
		},
			want: "trusted restore storage profile is unavailable"},
		{name: "profile without restore checks", change: func(_ *testing.T, r *run) {
			profile := r.f.profile
			profile.RequiredRestoreCheckIDs = nil
			r.f.workflow.profiles = provisionR3ProfileResolver{profile: profile}
		}, want: "trusted restore storage profile is unavailable"},
		{name: "request without restore", change: func(t *testing.T, r *run) {
			r.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, r.f.request, func(map[string]any) {})
		}, want: "persisted restore request is invalid"},
		{name: "backup no longer valid", change: func(_ *testing.T, r *run) {
			profile := r.f.profile
			profile.BackupStorageAdapterID = "storage.other"
			r.f.workflow.profiles = provisionR3ProfileResolver{profile: profile}
		}, want: "persisted restore backup no longer satisfies its exact trusted profile"},
		{name: "no created instance", change: func(_ *testing.T, r *run) { r.f.operation.Steps[1].Status = protocolstate.StepPending }, want: unbound},
		{name: "not compatible", change: func(_ *testing.T, r *run) { r.f.operation.Steps[0].Status = protocolstate.StepPending }, want: unbound},
		{name: "target other instance", change: restoreStep(func(s *store.OperationStep) {
			s.ResolvedInput = provisionR3MustEdit(s.ResolvedInput, "target", map[string]any{"instance_ref": map[string]any{"external_id": "vm-other"}})
		}), want: unbound},
		{name: "restore pending", change: restoreStep(func(s *store.OperationStep) { s.Status = protocolstate.StepPending }), want: unbound},
		{name: "restore recorded succeeded", change: func(_ *testing.T, r *run) {
			r.f.operation.Steps[6].Status = protocolstate.StepSucceeded
			r.step.Status = protocolstate.StepPending
		}, count: 1},
		{name: "object open fails", change: func(_ *testing.T, r *run) {
			r.f.workflow.backups = provisionR3Objects{staticProvisionBackupResolver: r.resolver, err: errors.New("object store offline")}
		}, want: "controller could not open an exact verified backup object"},
		{name: "object missing", change: func(_ *testing.T, r *run) {
			r.f.workflow.backups = provisionR3Objects{staticProvisionBackupResolver: r.resolver, none: true}
		}, want: "controller could not open an exact verified backup object"},
		{name: "deadline passed", change: func(_ *testing.T, r *run) { r.deadline = time.Now().Add(-time.Second) }, want: "restore stream deadline has expired"},
		{name: "streams", count: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f, resolver := provisionR3StreamSetup(t)
			r := &run{f: &f, resolver: resolver, ctx: context.Background(), step: f.operation.Steps[6], deadline: time.Now().Add(time.Minute)}
			if test.change != nil {
				test.change(t, r)
			}
			transfers, err := r.f.workflow.StreamTransfersForStep(r.ctx, r.f.operation, r.step, r.deadline)
			if got := provisionErrText(err); got != test.want || len(transfers) != test.count {
				t.Fatalf("StreamTransfersForStep = %d transfers, %q; want %d, %q", len(transfers), got, test.count, test.want)
			}
			provisionR3CloseTransfers(t, transfers)
		})
	}
}

func provisionR3CloseTransfers(t *testing.T, transfers []adapter.StreamTransfer) {
	t.Helper()
	for _, transfer := range transfers {
		if err := transfer.Source.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
