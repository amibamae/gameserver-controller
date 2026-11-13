package store

// Characterization tests for the lifecycle-driven capacity paths in
// capacity.go: boot readiness from server.start and confirmed release from
// server.destroy, both running inside the caller's Bolt transaction.

import (
	"encoding/json"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const capacityRefactorInstanceRef = `{"adapter_id":"hosting.vultr","environment_id":"staging","resource_kind":"instance","external_id":"vm-a"}`

func capacityRefactorLifecycle(t *testing.T, repository *BoltStore, operation OperationRecord, server LogicalServer) error {
	t.Helper()
	return repository.db.Update(func(tx *bolt.Tx) error {
		return recordCapacityLifecycleOutcomeTx(tx, operation, server)
	})
}

func capacityRefactorMarshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// capacityRefactorDestroyFixture returns a successful server.destroy record
// with exact absence and backup evidence for vm-a, and the destroyed server.
func capacityRefactorDestroyFixture(t *testing.T, observedAt time.Time) (OperationRecord, LogicalServer, map[string]any, map[string]any) {
	t.Helper()
	ref := json.RawMessage(capacityRefactorInstanceRef)
	data := map[string]any{
		"instance_ref": ref, "verified_backup_id": "backup-1", "data_generation_id": "gen-1",
		"provider_absence_confirmed": true, "final_lifecycle_state": "destroyed",
	}
	result := map[string]any{"operation_type": "server.destroy", "operation_data": data}
	absence := map[string]any{"instance_ref": ref, "state": "destroyed", "observed_at": observedAt}
	operation := OperationRecord{
		OperationType: "server.destroy", Status: "succeeded",
		Target: OperationTarget{ServerID: "srv", EnvironmentID: "staging", InstanceRef: ref},
		Result: capacityRefactorMarshal(t, result),
		Steps: []OperationStep{
			{StepID: "other", Status: "failed"},
			{StepID: "verify-instance-absence", Status: "succeeded", Output: capacityRefactorMarshal(t, absence)},
		},
	}
	server := LogicalServer{ServerID: "srv", EnvironmentID: "staging", LifecycleState: "destroyed", DataGenerationID: "gen-1"}
	return operation, server, result, absence
}

func capacityRefactorActiveWithAdapter(t *testing.T, repository *BoltStore, operationID, adapterID, instanceID string) CapacityReservation {
	t.Helper()
	request := capacityRequest(operationID, "srv", "staging", "g")
	request.ProviderAdapterID = adapterID
	reserved, _, err := repository.ReserveCapacity(request, 9, 9)
	if err != nil {
		t.Fatal(err)
	}
	return observePresent(t, repository, reserved, instanceID, reserved.CreatedAt.Add(time.Second), reserved.CreatedAt)
}

func TestCapacityRefactorDestroyEvidenceCharacterization(t *testing.T) {
	const (
		resultMsg   = capacityRefactorInvalid + ": server.destroy success lacks exact absence and backup evidence"
		absenceMsg  = capacityRefactorInvalid + ": server.destroy success lacks exact provider absence observation"
		instanceMsg = capacityRefactorInvalid + ": server.destroy target is not an exact provider instance"
		futureMsg   = capacityRefactorInvalid + ": provider absence observation is too far in the future"
	)
	otherRef := json.RawMessage(`{"adapter_id":"hosting.vultr","environment_id":"staging","resource_kind":"instance","external_id":"vm-b"}`)
	cases := map[string]struct {
		mutate  func(*OperationRecord, *LogicalServer, map[string]any, map[string]any)
		message string
	}{
		"result not json": {func(o *OperationRecord, _ *LogicalServer, _, _ map[string]any) { o.Result = json.RawMessage("x") }, resultMsg},
		"result type":     {func(_ *OperationRecord, _ *LogicalServer, r, _ map[string]any) { r["operation_type"] = "server.start" }, resultMsg},
		"result ref": {func(_ *OperationRecord, _ *LogicalServer, r, _ map[string]any) {
			r["operation_data"].(map[string]any)["instance_ref"] = otherRef
		}, resultMsg},
		"backup": {func(_ *OperationRecord, _ *LogicalServer, r, _ map[string]any) {
			r["operation_data"].(map[string]any)["verified_backup_id"] = ""
		}, resultMsg},
		"generation": {func(_ *OperationRecord, s *LogicalServer, _, _ map[string]any) { s.DataGenerationID = "gen-2" }, resultMsg},
		"absence flag": {func(_ *OperationRecord, _ *LogicalServer, r, _ map[string]any) {
			r["operation_data"].(map[string]any)["provider_absence_confirmed"] = false
		}, resultMsg},
		"final state": {func(_ *OperationRecord, _ *LogicalServer, r, _ map[string]any) {
			r["operation_data"].(map[string]any)["final_lifecycle_state"] = "ready"
		}, resultMsg},
		"result and absence both bad": {func(o *OperationRecord, _ *LogicalServer, r, _ map[string]any) {
			r["operation_type"] = "x"
			o.Steps = nil
		}, resultMsg},
		"no absence step": {func(o *OperationRecord, _ *LogicalServer, _, _ map[string]any) { o.Steps = o.Steps[:1] }, absenceMsg},
		"absence failed":  {func(o *OperationRecord, _ *LogicalServer, _, _ map[string]any) { o.Steps[1].Status = "failed" }, absenceMsg},
		"absence not json": {func(o *OperationRecord, _ *LogicalServer, _, _ map[string]any) {
			o.Steps[1].Output = json.RawMessage("x")
		}, absenceMsg},
		"absence ref":       {func(_ *OperationRecord, _ *LogicalServer, _, a map[string]any) { a["instance_ref"] = otherRef }, absenceMsg},
		"absence state":     {func(_ *OperationRecord, _ *LogicalServer, _, a map[string]any) { a["state"] = "absent" }, absenceMsg},
		"absence zero time": {func(_ *OperationRecord, _ *LogicalServer, _, a map[string]any) { a["observed_at"] = time.Time{} }, absenceMsg},
		"first absence step wins": {func(o *OperationRecord, _ *LogicalServer, _, _ map[string]any) {
			o.Steps = append([]OperationStep{{StepID: "verify-instance-absence", Status: "failed"}}, o.Steps...)
		}, absenceMsg},
		"future absence": {func(_ *OperationRecord, _ *LogicalServer, _, a map[string]any) {
			a["observed_at"] = time.Now().UTC().Add(10 * time.Minute)
		}, futureMsg},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repository := openTestStore(t)
			capacityRefactorActiveWithAdapter(t, repository, "op-a", "hosting.vultr", "vm-a")
			stored := capacityRefactorRawGet(t, repository, "op-a")
			operation, server, result, absence := capacityRefactorDestroyFixture(t, time.Now().UTC())
			tc.mutate(&operation, &server, result, absence)
			if name != "result not json" {
				operation.Result = capacityRefactorMarshal(t, result)
			}
			if name != "absence not json" && name != "no absence step" && name != "result and absence both bad" && name != "first absence step wins" {
				operation.Steps[1].Output = capacityRefactorMarshal(t, absence)
			}
			capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrInvalidRecord, tc.message)
			capacityRefactorAssertStored(t, repository, "op-a", stored)
		})
	}
	// Instance target checks need the target ref and evidence refs to agree.
	for name, ref := range map[string]string{
		"kind":     `{"adapter_id":"hosting.vultr","resource_kind":"volume","external_id":"vm-a"}`,
		"adapter":  `{"adapter_id":"","resource_kind":"instance","external_id":"vm-a"}`,
		"external": `{"adapter_id":"hosting.vultr","resource_kind":"instance"}`,
	} {
		t.Run("target "+name, func(t *testing.T) {
			repository := openTestStore(t)
			operation, server, result, absence := capacityRefactorDestroyFixture(t, time.Now().UTC().Add(10*time.Minute))
			operation.Target.InstanceRef = json.RawMessage(ref)
			result["operation_data"].(map[string]any)["instance_ref"] = json.RawMessage(ref)
			absence["instance_ref"] = json.RawMessage(ref)
			operation.Result = capacityRefactorMarshal(t, result)
			operation.Steps[1].Output = capacityRefactorMarshal(t, absence)
			// Target identity is checked before the future-time bound.
			capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrInvalidRecord, instanceMsg)
		})
	}
}

func TestCapacityRefactorDestroyReleaseCharacterization(t *testing.T) {
	t.Run("no reservation is a no-op", func(t *testing.T) {
		repository := openTestStore(t)
		operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC())
		if err := capacityRefactorLifecycle(t, repository, operation, server); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("other server is ignored", func(t *testing.T) {
		repository := openTestStore(t)
		other := capacityRequest("op-o", "srv-other", "staging", "g")
		reserved := reserve(t, repository, other)
		observePresent(t, repository, reserved, "vm-a", reserved.CreatedAt.Add(time.Second), time.Time{})
		stored := capacityRefactorRawGet(t, repository, "op-o")
		operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC())
		if err := capacityRefactorLifecycle(t, repository, operation, server); err != nil {
			t.Fatal(err)
		}
		capacityRefactorAssertStored(t, repository, "op-o", stored)
	})
	for _, state := range []CapacityState{CapacityActive, CapacityDestroyOutcomeUnknown} {
		for _, adapterID := range []string{"hosting.vultr", ""} {
			t.Run("releases "+string(state)+" adapter="+adapterID, func(t *testing.T) {
				repository := openTestStore(t)
				active := capacityRefactorActiveWithAdapter(t, repository, "op-a", adapterID, "vm-a")
				if state == CapacityDestroyOutcomeUnknown {
					if _, err := repository.MarkCapacityDestroySent("op-a"); err != nil {
						t.Fatal(err)
					}
				}
				observedAt := time.Now().UTC().Add(time.Minute)
				operation, server, _, _ := capacityRefactorDestroyFixture(t, observedAt)
				if err := capacityRefactorLifecycle(t, repository, operation, server); err != nil {
					t.Fatal(err)
				}
				loaded := capacityRefactorLoad(t, repository, "op-a")
				want := CapacityProviderObservation{Provider: "vultr", ResourceKey: active.ResourceKey, ProviderInstanceID: "vm-a", State: "destroyed", ObservedAt: observedAt}
				if loaded.State != CapacityDestroyed || loaded.ProviderInstanceID != "vm-a" || !loaded.UpdatedAt.Equal(observedAt) ||
					capacityRefactorJSON(t, *loaded.LastObservation) != capacityRefactorJSON(t, want) {
					t.Fatalf("released = %+v / %+v", loaded, loaded.LastObservation)
				}
				// Repeating the same destroy finds only a released reservation.
				stored := capacityRefactorRawGet(t, repository, "op-a")
				if err := capacityRefactorLifecycle(t, repository, operation, server); err != nil {
					t.Fatal(err)
				}
				capacityRefactorAssertStored(t, repository, "op-a", stored)
			})
		}
	}
	t.Run("adapter or instance mismatch", func(t *testing.T) {
		for name, setup := range map[string][2]string{"adapter": {"hosting.other", "vm-a"}, "instance": {"hosting.vultr", "vm-b"}} {
			t.Run(name, func(t *testing.T) {
				repository := openTestStore(t)
				capacityRefactorActiveWithAdapter(t, repository, "op-a", setup[0], setup[1])
				stored := capacityRefactorRawGet(t, repository, "op-a")
				operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC())
				capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrCapacityConflict,
					capacityRefactorConflictMsg+": managed VM reservation does not match the destroyed instance")
				capacityRefactorAssertStored(t, repository, "op-a", stored)
			})
		}
	})
	t.Run("multiple matches", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActiveWithAdapter(t, repository, "op-a", "hosting.vultr", "vm-a")
		duplicate := active
		duplicate.OperationID = "op-b"
		duplicate.ResourceKey = "gsc-dup"
		duplicate.LastObservation = &CapacityProviderObservation{Provider: "vultr", ResourceKey: "gsc-dup", ProviderInstanceID: "vm-a", State: "present", ObservedAt: active.LastObservation.ObservedAt}
		capacityRefactorPut(t, repository, duplicate)
		operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC())
		capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrCapacityConflict,
			capacityRefactorConflictMsg+": multiple active managed VM reservations match one destroyed server")
	})
	t.Run("released reservations are skipped before identity checks", func(t *testing.T) {
		repository := openTestStore(t)
		released := capacityRefactorActiveWithAdapter(t, repository, "op-0", "hosting.other", "vm-old")
		if _, err := repository.ObserveCapacity("op-0", capacityObservation(released, "absent", "", released.LastObservation.ObservedAt.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
		request := capacityRequest("op-a", "srv", "staging", "g")
		request.ResourceKey = "gsc-new"
		request.ProviderAdapterID = "hosting.vultr"
		reserved := reserve(t, repository, request)
		observePresent(t, repository, reserved, "vm-a", reserved.CreatedAt.Add(time.Second), time.Time{})
		operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC().Add(time.Minute))
		if err := capacityRefactorLifecycle(t, repository, operation, server); err != nil {
			t.Fatal(err)
		}
		if loaded := capacityRefactorLoad(t, repository, "op-a"); loaded.State != CapacityDestroyed {
			t.Fatalf("op-a = %+v", loaded)
		}
		if loaded := capacityRefactorLoad(t, repository, "op-0"); loaded.State != CapacityAbsent {
			t.Fatalf("op-0 = %+v", loaded)
		}
	})
	t.Run("unreleasable state", func(t *testing.T) {
		repository := openTestStore(t)
		reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		reserved.State = CapacityCreateOutcomeUnknown
		reserved.ProviderInstanceID = "vm-a"
		capacityRefactorPut(t, repository, reserved)
		operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC())
		capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrCapacityTransition,
			`invalid capacity reservation transition: cannot release managed VM capacity from state "create_outcome_unknown"`)
	})
	t.Run("observation conflict propagates", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActiveWithAdapter(t, repository, "op-a", "hosting.vultr", "vm-a")
		operation, server, _, _ := capacityRefactorDestroyFixture(t, active.LastObservation.ObservedAt.Add(-time.Nanosecond))
		capacityRefactorAssertSentinel(t, capacityRefactorLifecycle(t, repository, operation, server), ErrCapacityConflict)
	})
	t.Run("corrupt and key mismatch", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActiveWithAdapter(t, repository, "op-a", "hosting.vultr", "vm-a")
		capacityRefactorRawPut(t, repository, "op-alias", []byte(capacityRefactorJSON(t, active)))
		operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC())
		capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrCorruptStore,
			capacityRefactorCorrupt+": capacity reservation key mismatch")
		capacityRefactorRawPut(t, repository, "op-alias", []byte("{"))
		capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrCorruptStore, "")
	})
	t.Run("not succeeded or not destroyed is ignored", func(t *testing.T) {
		repository := openTestStore(t)
		operation, server, _, _ := capacityRefactorDestroyFixture(t, time.Now().UTC())
		operation.Result = json.RawMessage("x")
		failed := operation
		failed.Status = "failed"
		if err := capacityRefactorLifecycle(t, repository, failed, server); err != nil {
			t.Fatal(err)
		}
		ready := server
		ready.LifecycleState = "ready"
		if err := capacityRefactorLifecycle(t, repository, operation, ready); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCapacityRefactorStartReadinessCharacterization(t *testing.T) {
	ref := json.RawMessage(capacityRefactorInstanceRef)
	startResult := func(t *testing.T, serverID, environmentID string, instanceRef json.RawMessage, observedAt time.Time) json.RawMessage {
		return capacityRefactorMarshal(t, map[string]any{"operation_data": map[string]any{"readiness_assertion": map[string]any{
			"server_id": serverID, "environment_id": environmentID, "instance_ref": instanceRef, "observed_at": observedAt,
		}}})
	}
	server := LogicalServer{ServerID: "srv", EnvironmentID: "staging", InstanceRef: ref, LifecycleState: "ready"}
	setup := func(t *testing.T) (*BoltStore, CapacityReservation, []byte) {
		repository := openTestStore(t)
		active := capacityRefactorActiveWithAdapter(t, repository, "op-a", "hosting.vultr", "vm-a")
		return repository, active, capacityRefactorRawGet(t, repository, "op-a")
	}
	t.Run("ignored inputs", func(t *testing.T) {
		repository, active, stored := setup(t)
		at := active.LastObservation.ObservedAt.Add(time.Second)
		good := startResult(t, "srv", "staging", ref, at)
		for name, call := range map[string]struct {
			operation OperationRecord
			server    LogicalServer
		}{
			"other type":     {OperationRecord{OperationType: "server.stop", Status: "succeeded", Result: good}, server},
			"not succeeded":  {OperationRecord{OperationType: "server.start", Status: "failed", Result: good}, server},
			"not ready":      {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: good}, LogicalServer{ServerID: "srv", EnvironmentID: "staging", InstanceRef: ref, LifecycleState: "stopped"}},
			"bad result":     {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: json.RawMessage("x")}, server},
			"server id":      {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "other", "staging", ref, at)}, server},
			"environment id": {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "prod", ref, at)}, server},
			"instance ref":   {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "staging", json.RawMessage(`{"external_id":"vm-a"}`), at)}, server},
			"zero time":      {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "staging", ref, time.Time{})}, server},
			"no adapter": {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "staging", json.RawMessage(`{"external_id":"vm-a"}`), at)},
				LogicalServer{ServerID: "srv", EnvironmentID: "staging", InstanceRef: json.RawMessage(`{"external_id":"vm-a"}`), LifecycleState: "ready"}},
			"no external": {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "staging", json.RawMessage(`{"adapter_id":"hosting.vultr"}`), at)},
				LogicalServer{ServerID: "srv", EnvironmentID: "staging", InstanceRef: json.RawMessage(`{"adapter_id":"hosting.vultr"}`), LifecycleState: "ready"}},
			"not found":  {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv-x", "staging", ref, at)}, LogicalServer{ServerID: "srv-x", EnvironmentID: "staging", InstanceRef: ref, LifecycleState: "ready"}},
			"transition": {OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "staging", ref, active.CreatedAt.Add(-time.Second))}, server},
		} {
			if err := capacityRefactorLifecycle(t, repository, call.operation, call.server); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			capacityRefactorAssertStored(t, repository, "op-a", stored)
		}
	})
	t.Run("records readiness and ignores conflicting repeat", func(t *testing.T) {
		repository, active, _ := setup(t)
		at := active.LastObservation.ObservedAt.Add(time.Second)
		operation := OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "staging", ref, at)}
		if err := capacityRefactorLifecycle(t, repository, operation, server); err != nil {
			t.Fatal(err)
		}
		loaded := capacityRefactorLoad(t, repository, "op-a")
		if !loaded.ReadyAt.Equal(at) {
			t.Fatalf("ready = %+v", loaded)
		}
		stored := capacityRefactorRawGet(t, repository, "op-a")
		operation.Result = startResult(t, "srv", "staging", ref, at.Add(time.Second))
		if err := capacityRefactorLifecycle(t, repository, operation, server); err != nil {
			t.Fatalf("conflicting repeat: %v", err)
		}
		capacityRefactorAssertStored(t, repository, "op-a", stored)
	})
	t.Run("corrupt store propagates", func(t *testing.T) {
		repository, active, _ := setup(t)
		capacityRefactorRawPut(t, repository, "op-z", []byte("{"))
		operation := OperationRecord{OperationType: "server.start", Status: "succeeded", Result: startResult(t, "srv", "staging", ref, active.LastObservation.ObservedAt)}
		capacityRefactorAssertErr(t, capacityRefactorLifecycle(t, repository, operation, server), ErrCorruptStore, "")
	})
}
