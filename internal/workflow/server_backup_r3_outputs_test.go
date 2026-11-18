package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin every recorded-output rule of the backup
// workflow. Each case changes one field of a fully recorded backup and lists
// exactly which rules change their answer, so a rule that loses or gains a
// clause is caught.

// backupR3Evidence evaluates every output rule against operation.
func backupR3Evidence(t *testing.T, f backupR3Fixture, operation store.OperationRecord) map[string]string {
	t.Helper()
	workflow := f.backupR3Workflow(f.server, backupR3Streams{})
	_, profile, err := workflow.loadProfile(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	_, err = backupPrepareOutput(operation)
	result["prepare"] = backupR3ErrText(err)
	_, err = backupCaptureOutput(operation, "world")
	result["capture"] = backupR3ErrText(err)
	_, err = backupPutOutput(operation, "world")
	result["put"] = backupR3ErrText(err)
	_, err = backupGetOutput(operation, "world")
	result["get"] = backupR3ErrText(err)
	_, err = backupRuntimeStopOutput(operation)
	result["stop"] = backupR3ErrText(err)
	unit, unitErr := backupRuntimeInspection(operation, backupInspectRuntimeStep)
	result["inspection"] = backupR3ErrText(unitErr)
	for name, value := range map[string]bool{
		"describe":            backupDataDescriptionMatches(operation, profile, f.server),
		"describeInput":       backupDataDescribeInputMatches(operation, profile, f.server),
		"save":                gameBackupSaveConfirmed(operation),
		"readbacks":           backupReadbacksComplete(operation, profile),
		"validation":          backupValidationConfirmed(operation, profile),
		"start":               backupRuntimeStartCompleted(operation),
		"running":             backupRuntimeRunning(operation),
		"finalStopped":        backupFinalRuntimeStopped(operation),
		"runtimeExpected":     runtimeExpectedForBackup(operation, unit, unitErr),
		"saveRejected":        backupSaveRejected(operation),
		"preparationRejected": backupPreparationRejected(operation),
		"captureRejected":     backupCaptureRejected(operation, "world"),
		"validationRejected":  backupValidationRejected(operation),
		"stopSucceeded":       backupRuntimeStopOutputSucceeded(operation),
		"initiallyActive":     backupInitiallyActive(operation),
	} {
		result[name] = fmt.Sprint(value)
	}
	return result
}

// backupR3EvidenceDiff lists the rules whose answer differs from base.
func backupR3EvidenceDiff(base, changed map[string]string) string {
	var keys []string
	for key := range changed {
		if changed[key] != base[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+changed[key])
	}
	return strings.Join(parts, "; ")
}

func backupR3RunEvidence(t *testing.T, resume bool, baseChange func(*testing.T, *store.OperationRecord), wantBase string, tests []struct {
	name   string
	change func(*testing.T, *store.OperationRecord)
	want   string
}) {
	t.Helper()
	f := newBackupR3Fixture(t, resume)
	base := backupR3Copy(f.complete)
	if baseChange != nil {
		baseChange(t, &base)
	}
	baseline := backupR3Evidence(t, f, base)
	if wantBase != "" {
		if got := backupR3EvidenceDiff(backupR3Evidence(t, f, f.complete), baseline); got != wantBase {
			t.Fatalf("baseline evidence diff = %q, want %q", got, wantBase)
		}
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := backupR3Copy(base)
			test.change(t, &operation)
			if got := backupR3EvidenceDiff(baseline, backupR3Evidence(t, f, operation)); got != test.want {
				t.Errorf("evidence diff = %q\nwant            %q", got, test.want)
			}
		})
	}
}

type backupR3EvidenceCase = struct {
	name   string
	change func(*testing.T, *store.OperationRecord)
	want   string
}

func TestBackupR3CompleteEvidenceBaseline(t *testing.T) {
	for _, resume := range []bool{false, true} {
		f := newBackupR3Fixture(t, resume)
		got := backupR3Evidence(t, f, f.complete)
		want := map[string]string{
			"prepare": "<nil>", "capture": "<nil>", "put": "<nil>", "get": "<nil>", "stop": "<nil>", "inspection": "<nil>",
			"describe": "true", "describeInput": "true", "save": "true", "readbacks": "true", "validation": "true",
			"start": fmt.Sprint(resume), "running": fmt.Sprint(resume), "finalStopped": fmt.Sprint(!resume),
			"runtimeExpected": "true", "saveRejected": "false", "preparationRejected": "false", "captureRejected": "false",
			"validationRejected": "false", "stopSucceeded": "true", "initiallyActive": "true",
		}
		if diff := backupR3EvidenceDiff(want, got); diff != "" || len(got) != len(want) {
			t.Fatalf("resume=%v baseline evidence differs: %s", resume, diff)
		}
	}
}

func TestBackupR3PreparationAndCaptureOutputs(t *testing.T) {
	const prepBinding = "prepare=backup preparation output does not bind the exact request"
	const captureBinding = "capture=capture output does not bind the exact prepared data set; readbacks=false"
	backupR3RunEvidence(t, false, nil, "", []backupR3EvidenceCase{
		{"prepare missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupPrepareStep) },
			"prepare=backup preparation output is missing"},
		{"prepare pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupPrepareStep) },
			"prepare=backup preparation output is missing"},
		{"prepare output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupPrepareStep, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, "prepare=backup preparation output is missing"},
		{"prepare input not an object", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupPrepareStep, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`"x"`) })
		}, prepBinding},
		{"prepare outcome", backupR3Field(backupPrepareStep, "outcome", "rejected"), prepBinding},
		{"prepare target", backupR3Field(backupPrepareStep, "target", json.RawMessage(backupR3OtherTarget)), prepBinding},
		{"prepare release", backupR3Field(backupPrepareStep, "release_id", "other"), prepBinding},
		{"prepare backup", backupR3Field(backupPrepareStep, "backup_id", "other"), prepBinding},
		{"prepare generation", backupR3Field(backupPrepareStep, "data_generation_id", "other"), prepBinding},
		{"prepare consistency", backupR3Field(backupPrepareStep, "consistency_mode", "online"), prepBinding},
		{"prepare data sets", backupR3Field(backupPrepareStep, "data_set_ids", []string{"other"}), prepBinding},
		{"prepare preparation ID", backupR3Field(backupPrepareStep, "preparation_id", ""), prepBinding},
		{"prepare evidence", backupR3Field(backupPrepareStep, "consistency_evidence", ""), prepBinding},
		{"prepare observed", backupR3Field(backupPrepareStep, "observed_at", "yesterday"),
			`prepare=parsing time "yesterday" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "yesterday" as "2006"`},

		{"capture missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupR3Capture) },
			"capture=capture output is missing; readbacks=false"},
		{"capture pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Capture) },
			"capture=capture output is missing; readbacks=false"},
		{"capture output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupR3Capture, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, "capture=capture output is missing; readbacks=false"},
		{"capture input not an object", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupR3Capture, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`"x"`) })
		}, captureBinding},
		{"capture outcome", backupR3Field(backupR3Capture, "outcome", "rejected"), captureBinding},
		{"capture stream", backupR3Field(backupR3Capture, "stream_completed", false), captureBinding},
		{"capture target", backupR3Field(backupR3Capture, "target", json.RawMessage(backupR3OtherTarget)), captureBinding},
		{"capture release", backupR3Field(backupR3Capture, "release_id", "other"), captureBinding},
		{"capture backup", backupR3Field(backupR3Capture, "backup_id", "other"), captureBinding},
		{"capture generation", backupR3Field(backupR3Capture, "data_generation_id", "other"), captureBinding},
		{"capture preparation", backupR3Field(backupR3Capture, "preparation_id", "other"), captureBinding},
		{"capture data set", backupR3Field(backupR3Capture, "data_set_id", "other"), captureBinding},
		{"capture ID", backupR3Field(backupR3Capture, "capture_id", "other"), captureBinding},
		{"capture consistency", backupR3Field(backupR3Capture, "consistency_mode", "online"), captureBinding},
		{"capture negative size", backupR3Field(backupR3Capture, "size_bytes", -1), captureBinding},
		{"capture algorithm", backupR3Field(backupR3Capture, "checksum", map[string]any{"algorithm": "md5", "digest": backupTestDigest}), captureBinding},
		{"capture short digest", backupR3Field(backupR3Capture, "checksum", map[string]any{"algorithm": "sha256", "digest": "abcd"}), captureBinding},
		{"capture non-hex digest", backupR3Field(backupR3Capture, "checksum", map[string]any{"algorithm": "sha256", "digest": strings.Repeat("z", 64)}),
			"capture=capture output checksum is not hexadecimal SHA-256; readbacks=false"},
		{"capture observed", backupR3Field(backupR3Capture, "observed_at", "yesterday"),
			`capture=parsing time "yesterday" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "yesterday" as "2006"; readbacks=false`},
		{"capture oversized", backupR3Field(backupR3Capture, "size_bytes", 65), "readbacks=false"},
	})
}

func TestBackupR3StorageOutputs(t *testing.T) {
	const putBinding = "put=object upload output does not bind the exact target; readbacks=false"
	const getBinding = "get=object readback output does not bind the exact object; readbacks=false"
	otherRef := json.RawMessage(`{"adapter_id":"storage.fake","container":"other","environment_id":"staging","key":"k","storage_profile_id":"storage-profile"}`)
	backupR3RunEvidence(t, false, nil, "", []backupR3EvidenceCase{
		{"put missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupR3Upload) },
			"put=object upload output is missing; readbacks=false"},
		{"put output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupR3Upload, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, "put=object upload output is missing; readbacks=false"},
		{"put input not an object", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupR3Upload, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`"x"`) })
		}, putBinding},
		{"put target", backupR3Field(backupR3Upload, "target", otherRef), putBinding},
		{"put object in another container", backupR3Field(backupR3Upload, "object_ref", otherRef), putBinding},
		{"put object not an object", backupR3Field(backupR3Upload, "object_ref", []any{}), putBinding},
		{"put object empty", backupR3Field(backupR3Upload, "object_ref", map[string]any{}), putBinding},
		{"put negative bytes", backupR3Field(backupR3Upload, "bytes_received", -1), putBinding},
		{"put observed", backupR3Field(backupR3Upload, "observed_at", "yesterday"),
			`put=parsing time "yesterday" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "yesterday" as "2006"; readbacks=false`},
		{"put partial", backupR3Field(backupR3Upload, "upload_state", "partial"), "readbacks=false"},
		{"put short", backupR3Field(backupR3Upload, "bytes_received", 31), "readbacks=false"},

		{"get missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupR3Readback) },
			"get=object readback output is missing; readbacks=false"},
		{"get output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupR3Readback, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, "get=object readback output is missing; readbacks=false"},
		{"get input not an object", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupR3Readback, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`"x"`) })
		}, getBinding},
		{"get object", backupR3Field(backupR3Readback, "object_ref", otherRef), getBinding},
		{"get bytes missing", backupR3Field(backupR3Readback, "bytes_sent", nil), getBinding},
		{"get observed", backupR3Field(backupR3Readback, "observed_at", "yesterday"),
			`get=parsing time "yesterday" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "yesterday" as "2006"; readbacks=false`},
		{"get partial", backupR3Field(backupR3Readback, "transfer_state", "partial"), "readbacks=false"},
		{"get short", backupR3Field(backupR3Readback, "bytes_sent", 31), "readbacks=false"},
		{"get algorithm", backupR3Field(backupR3Readback, "computed_checksum", map[string]any{"algorithm": "md5", "digest": backupTestDigest}), "readbacks=false"},
		{"get digest", backupR3Field(backupR3Readback, "computed_checksum", map[string]any{"algorithm": "sha256", "digest": backupR3OtherDigest}), "readbacks=false"},
		{"get object of another upload", func(t *testing.T, o *store.OperationRecord) {
			ref := json.RawMessage(`{"adapter_id":"storage.fake","container":"backups","environment_id":"staging","key":"server-1/` + o.OperationID + `/world","storage_profile_id":"storage-profile","version_id":"v2"}`)
			setBackupOutputField(t, o, backupR3Readback, "object_ref", ref)
			backupR3SetInputField(t, o, backupR3Readback, "object_ref", ref)
		}, "readbacks=false"},
	})
}

func TestBackupR3DescriptionAndSaveOutputs(t *testing.T) {
	trusted := map[string]any{"data_set_id": "world", "relative_paths": []string{"world"}, "required": true, "supported_consistency_modes": []string{"offline"}}
	dataSet := func(field string, value any) []any {
		changed := map[string]any{}
		for key, item := range trusted {
			changed[key] = item
		}
		changed[field] = value
		return []any{changed}
	}
	const describe = "describe=false"
	backupR3RunEvidence(t, false, nil, "", []backupR3EvidenceCase{
		{"describe missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupDescribeDataStep) },
			"describe=false; describeInput=false"},
		{"describe pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupDescribeDataStep) }, describe},
		{"describe input not an object", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupDescribeDataStep, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`"x"`) })
		}, "describe=false; describeInput=false"},
		{"describe output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupDescribeDataStep, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, describe},
		{"describe output target", backupR3Field(backupDescribeDataStep, "target", json.RawMessage(backupR3OtherTarget)), describe},
		{"describe both targets", func(t *testing.T, o *store.OperationRecord) {
			setBackupOutputField(t, o, backupDescribeDataStep, "target", json.RawMessage(backupR3OtherTarget))
			backupR3SetInputField(t, o, backupDescribeDataStep, "target", json.RawMessage(backupR3OtherTarget))
		}, "describe=false; describeInput=false"},
		{"describe input release", backupR3InputField(backupDescribeDataStep, "release_id", "other"), "describe=false; describeInput=false"},
		{"describe output release", backupR3Field(backupDescribeDataStep, "release_id", "other"), describe},
		{"describe no data sets", backupR3Field(backupDescribeDataStep, "data_sets", []any{}), describe},
		{"describe observed", backupR3Field(backupDescribeDataStep, "observed_at", "yesterday"), describe},
		{"describe data set ID", backupR3Field(backupDescribeDataStep, "data_sets", dataSet("data_set_id", "other")), describe},
		{"describe required", backupR3Field(backupDescribeDataStep, "data_sets", dataSet("required", false)), describe},
		{"describe format", backupR3Field(backupDescribeDataStep, "data_sets", dataSet("data_format", "anvil")), describe},
		{"describe paths", backupR3Field(backupDescribeDataStep, "data_sets", dataSet("relative_paths", []string{"world", "nether"})), describe},
		{"describe modes", backupR3Field(backupDescribeDataStep, "data_sets", dataSet("supported_consistency_modes", []string{"online"})), describe},
		{"describe input server", backupR3InputField(backupDescribeDataStep, "target", map[string]any{"server_id": "other", "environment_id": "staging", "instance_ref": json.RawMessage(backupTestInstance)}),
			"describe=false; describeInput=false"},
		{"describe input environment", backupR3InputField(backupDescribeDataStep, "target", map[string]any{"server_id": "server-1", "environment_id": "other", "instance_ref": json.RawMessage(backupTestInstance)}),
			"describe=false; describeInput=false"},
		{"describe input instance", backupR3InputField(backupDescribeDataStep, "target", json.RawMessage(backupR3OtherTarget)),
			"describe=false; describeInput=false"},

		{"save missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupSaveGameStep) }, "save=false"},
		{"save pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupSaveGameStep) }, "save=false"},
		{"save input not an object", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupSaveGameStep, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`"x"`) })
		}, "save=false"},
		{"save output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupSaveGameStep, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, "save=false"},
		{"save outcome", backupR3Field(backupSaveGameStep, "outcome", "rejected"), "save=false"},
		{"save server", backupR3Field(backupSaveGameStep, "target", map[string]any{"server_id": "other", "environment_id": "staging", "instance_ref": json.RawMessage(backupTestInstance)}), "save=false"},
		{"save environment", backupR3Field(backupSaveGameStep, "target", map[string]any{"server_id": "server-1", "environment_id": "other", "instance_ref": json.RawMessage(backupTestInstance)}), "save=false"},
		{"save release", backupR3Field(backupSaveGameStep, "release_id", "other"), "save=false"},
		{"save output instance", backupR3Field(backupSaveGameStep, "target", json.RawMessage(backupR3OtherTarget)), "save=false"},
		{"save both instances", func(t *testing.T, o *store.OperationRecord) {
			setBackupOutputField(t, o, backupSaveGameStep, "target", json.RawMessage(backupR3OtherTarget))
			backupR3SetInputField(t, o, backupSaveGameStep, "target", json.RawMessage(backupR3OtherTarget))
		}, "save=false"},
		{"save observed", backupR3Field(backupSaveGameStep, "observed_at", "yesterday"), "save=false"},
		{"save saved", backupR3Field(backupSaveGameStep, "saved_at", "yesterday"), "save=false"},
	})
}

func TestBackupR3RuntimeAndValidationOutputs(t *testing.T) {
	const stopFailed = "runtimeExpected=false; stop=runtime stop output does not confirm the exact unit stopped; stopSucceeded=false"
	const validation = "validation=false"
	backupR3RunEvidence(t, false, nil, "", []backupR3EvidenceCase{
		{"stop missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupStopRuntimeStep) }, stopFailed},
		{"stop pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupStopRuntimeStep) }, stopFailed},
		{"stop output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupStopRuntimeStep, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, stopFailed},
		{"stop outcome", backupR3Field(backupStopRuntimeStep, "outcome", "accepted"), stopFailed},
		{"stop state", backupR3Field(backupStopRuntimeStep, "state", "stopping"), stopFailed},
		{"stop unit", backupR3Field(backupStopRuntimeStep, "unit_ref", json.RawMessage(backupOtherUnit)), stopFailed},

		{"inspection unit", backupR3Field(backupInspectRuntimeStep, "unit_ref", json.RawMessage(backupOtherUnit)), "runtimeExpected=false"},
		{"inspection state", backupR3Field(backupInspectRuntimeStep, "state", "stopped"), "runtimeExpected=false"},
		{"inspection observed", backupR3Field(backupInspectRuntimeStep, "observed_at", "yesterday"),
			`inspection=parsing time "yesterday" as "2006-01-02T15:04:05.999999999Z07:00": cannot parse "yesterday" as "2006"; runtimeExpected=false`},
		{"inspection missing", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupInspectRuntimeStep) },
			"inspection=execution unit inspection output is missing; runtimeExpected=false"},
		{"initially stopped", func(_ *testing.T, o *store.OperationRecord) {
			o.BackupInitialLifecycleState = protocolstate.ServerStopped
		},
			"initiallyActive=false; runtimeExpected=false"},
		{"initially degraded", func(_ *testing.T, o *store.OperationRecord) {
			o.BackupInitialLifecycleState = protocolstate.ServerDegraded
		}, ""},
		{"initial state inferred from the stop step", func(_ *testing.T, o *store.OperationRecord) { o.BackupInitialLifecycleState = "" }, ""},
		{"initial state inferred without a stop step", func(_ *testing.T, o *store.OperationRecord) {
			o.BackupInitialLifecycleState = ""
			backupR3RemoveStep(o, backupStopRuntimeStep)
		}, "initiallyActive=false; runtimeExpected=false; stop=runtime stop output does not confirm the exact unit stopped; stopSucceeded=false"},

		{"validation missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupValidateStep) }, validation},
		{"validation pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupValidateStep) }, validation},
		{"validation output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupValidateStep, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, validation},
		{"validation input not an object", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupValidateStep, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`"x"`) })
		}, validation},
		{"validation result", backupR3Field(backupValidateStep, "validation_result", "invalid"), validation},
		{"validation backup", backupR3Field(backupValidateStep, "backup_id", "other"), validation},
		{"validation environment", backupR3Field(backupValidateStep, "environment_id", "other"), validation},
		{"validation generation", backupR3Field(backupValidateStep, "data_generation_id", "other"), validation},
		{"validation manifest backup", func(t *testing.T, o *store.OperationRecord) {
			manifest := decodeObject(stepField(effectiveStepInput(operationStep(*o, backupValidateStep)), "manifest"))
			manifest["backup_id"] = "other"
			backupR3SetInputField(t, o, backupValidateStep, "manifest", manifest)
		}, validation},
		{"validation manifest environment", func(t *testing.T, o *store.OperationRecord) {
			manifest := decodeObject(stepField(effectiveStepInput(operationStep(*o, backupValidateStep)), "manifest"))
			manifest["environment_id"] = "other"
			backupR3SetInputField(t, o, backupValidateStep, "manifest", manifest)
		}, validation},
		{"validation no checks", backupR3Field(backupValidateStep, "check_results", []any{}), validation},
		{"validation unknown check", backupR3Field(backupValidateStep, "check_results", []any{map[string]any{"check_id": "other", "check_version": "1", "result": "pass", "observed_at": backupNow()}}), validation},
		{"validation check version", backupR3Field(backupValidateStep, "check_results", []any{map[string]any{"check_id": "paper.backup.archive", "check_version": "2", "result": "pass", "observed_at": backupNow()}}), validation},
		{"validation failed check", backupR3Field(backupValidateStep, "check_results", []any{map[string]any{"check_id": "paper.backup.archive", "check_version": "1", "result": "fail", "observed_at": backupNow()}}), validation},
		{"validation check observed", backupR3Field(backupValidateStep, "check_results", []any{map[string]any{"check_id": "paper.backup.archive", "check_version": "1", "result": "pass", "observed_at": "yesterday"}}), validation},
		{"validation observed", backupR3Field(backupValidateStep, "observed_at", "yesterday"), validation},

		{"final inspection missing", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupVerifyStoppedStep) }, "finalStopped=false"},
		{"final inspection running", backupR3Field(backupVerifyStoppedStep, "state", "running"), "finalStopped=false"},
		{"final inspection absent", backupR3Field(backupVerifyStoppedStep, "state", "absent"), ""},
		{"final inspection unit", backupR3Field(backupVerifyStoppedStep, "unit_ref", json.RawMessage(backupOtherUnit)), "finalStopped=false"},
	})
}

func TestBackupR3ResumedRuntimeOutputs(t *testing.T) {
	backupR3RunEvidence(t, true, nil, "", []backupR3EvidenceCase{
		{"start missing", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupStartRuntimeStep) }, "start=false"},
		{"start output not JSON", func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupStartRuntimeStep, func(s *store.OperationStep) { s.Output = json.RawMessage(`nope`) })
		}, "start=false"},
		{"start outcome", backupR3Field(backupStartRuntimeStep, "outcome", "accepted"), "start=false"},
		{"start state", backupR3Field(backupStartRuntimeStep, "state", "starting"), "start=false"},
		{"start unit", backupR3Field(backupStartRuntimeStep, "unit_ref", json.RawMessage(backupOtherUnit)), "start=false"},
		{"start input unit", backupR3InputField(backupStartRuntimeStep, "unit_ref", json.RawMessage(backupOtherUnit)), "running=false; start=false"},
		{"verify state", backupR3Field(backupVerifyRuntimeStep, "state", "stopped"), "running=false"},
		{"verify unit", backupR3Field(backupVerifyRuntimeStep, "unit_ref", json.RawMessage(backupOtherUnit)), "running=false"},
		{"verify observed", backupR3Field(backupVerifyRuntimeStep, "observed_at", "yesterday"), "running=false"},
	})
}

// backupR3Rejected turns the complete backup into one where stepID recorded
// a valid rejection.
func backupR3Rejected(stepID string) func(*testing.T, *store.OperationRecord) {
	return func(t *testing.T, o *store.OperationRecord) {
		input := decodeObject(effectiveStepInput(operationStep(*o, stepID)))
		output := map[string]any{"outcome": "rejected", "reason_code": "test.rejected", "observed_at": backupNow()}
		for _, key := range []string{"target", "release_id", "backup_id", "data_generation_id", "data_set_ids", "consistency_mode", "preparation_id", "data_set_id", "capture_id"} {
			if value, ok := input[key]; ok {
				output[key] = value
			}
		}
		if stepID == backupValidateStep {
			manifest := asStringMap(input["manifest"])
			output = map[string]any{"backup_id": o.OperationID, "environment_id": "staging", "data_generation_id": manifest["data_generation_id"],
				"validation_result": "invalid", "observed_at": backupNow(), "check_results": []any{
					map[string]any{"check_id": "paper.backup.archive", "check_version": "1", "result": "fail", "observed_at": backupNow()},
					map[string]any{"check_id": "paper.backup.other", "check_version": "1", "result": "pass", "observed_at": backupNow()},
				}}
		}
		backupR3SetStep(o, stepID, func(s *store.OperationStep) { s.Output = backupJSON(t, output) })
	}
}

func TestBackupR3SaveRejection(t *testing.T) {
	const off = "saveRejected=false"
	backupR3RunEvidence(t, false, backupR3Rejected(backupSaveGameStep), "save=false; saveRejected=true", []backupR3EvidenceCase{
		{"pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupSaveGameStep) }, off},
		{"missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupSaveGameStep) }, off},
		{"outcome", backupR3Field(backupSaveGameStep, "outcome", "confirmed"), off},
		{"reason", backupR3Field(backupSaveGameStep, "reason_code", ""), off},
		{"release", backupR3Field(backupSaveGameStep, "release_id", "other"), off},
		{"target", backupR3Field(backupSaveGameStep, "target", json.RawMessage(backupR3OtherTarget)), off},
		{"both targets", func(t *testing.T, o *store.OperationRecord) {
			setBackupOutputField(t, o, backupSaveGameStep, "target", json.RawMessage(backupR3OtherTarget))
			backupR3SetInputField(t, o, backupSaveGameStep, "target", json.RawMessage(backupR3OtherTarget))
		}, off},
		{"target server", func(t *testing.T, o *store.OperationRecord) {
			target := map[string]any{"server_id": "other", "environment_id": "staging", "instance_ref": json.RawMessage(backupTestInstance)}
			setBackupOutputField(t, o, backupSaveGameStep, "target", target)
			backupR3SetInputField(t, o, backupSaveGameStep, "target", target)
		}, off},
		{"target environment", func(t *testing.T, o *store.OperationRecord) {
			target := map[string]any{"server_id": "server-1", "environment_id": "other", "instance_ref": json.RawMessage(backupTestInstance)}
			setBackupOutputField(t, o, backupSaveGameStep, "target", target)
			backupR3SetInputField(t, o, backupSaveGameStep, "target", target)
		}, off},
		{"target not an object", func(t *testing.T, o *store.OperationRecord) {
			setBackupOutputField(t, o, backupSaveGameStep, "target", "x")
			backupR3SetInputField(t, o, backupSaveGameStep, "target", "x")
		}, off},
		{"observed", backupR3Field(backupSaveGameStep, "observed_at", "yesterday"), off},
		{"zero observed", backupR3Field(backupSaveGameStep, "observed_at", "0001-01-01T00:00:00Z"), off},
	})
}

func TestBackupR3PreparationRejection(t *testing.T) {
	const off = "preparationRejected=false"
	backupR3RunEvidence(t, false, backupR3Rejected(backupPrepareStep),
		"preparationRejected=true; prepare=backup preparation output does not bind the exact request", []backupR3EvidenceCase{
			{"pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupPrepareStep) },
				"preparationRejected=false; prepare=backup preparation output is missing"},
			{"outcome", backupR3Field(backupPrepareStep, "outcome", "prepared"), off},
			{"reason", backupR3Field(backupPrepareStep, "reason_code", ""), off},
			{"backup", backupR3Field(backupPrepareStep, "backup_id", "other"), off},
			{"generation", backupR3Field(backupPrepareStep, "data_generation_id", "other"), off},
			{"release", backupR3Field(backupPrepareStep, "release_id", "other"), off},
			{"consistency", backupR3Field(backupPrepareStep, "consistency_mode", "online"), off},
			{"data sets", backupR3Field(backupPrepareStep, "data_set_ids", []string{"world", "nether"}), off},
			{"target", backupR3Field(backupPrepareStep, "target", json.RawMessage(backupR3OtherTarget)), off},
			{"both targets", func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupPrepareStep, "target", json.RawMessage(backupR3OtherTarget))
				backupR3SetInputField(t, o, backupPrepareStep, "target", json.RawMessage(backupR3OtherTarget))
			}, off},
			{"observed", backupR3Field(backupPrepareStep, "observed_at", "yesterday"), off},
		})
}

func TestBackupR3CaptureRejection(t *testing.T) {
	const off = "captureRejected=false"
	backupR3RunEvidence(t, false, backupR3Rejected(backupR3Capture),
		"capture=capture output does not bind the exact prepared data set; captureRejected=true; readbacks=false", []backupR3EvidenceCase{
			{"pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Capture) },
				"capture=capture output is missing; captureRejected=false"},
			{"outcome", backupR3Field(backupR3Capture, "outcome", "captured"), off},
			{"reason", backupR3Field(backupR3Capture, "reason_code", ""), off},
			{"backup", backupR3Field(backupR3Capture, "backup_id", "other"), off},
			{"generation", backupR3Field(backupR3Capture, "data_generation_id", "other"), off},
			{"preparation", backupR3Field(backupR3Capture, "preparation_id", "other"), off},
			{"data set", backupR3Field(backupR3Capture, "data_set_id", "other"), off},
			{"data set in input and output", func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupR3Capture, "data_set_id", "other")
				backupR3SetInputField(t, o, backupR3Capture, "data_set_id", "other")
			}, off},
			{"data set in input", backupR3InputField(backupR3Capture, "data_set_id", "other"), off},
			{"capture ID", backupR3Field(backupR3Capture, "capture_id", "other"), off},
			{"release", backupR3Field(backupR3Capture, "release_id", "other"), off},
			{"consistency", backupR3Field(backupR3Capture, "consistency_mode", "online"), off},
			{"target", backupR3Field(backupR3Capture, "target", json.RawMessage(backupR3OtherTarget)), off},
			{"both targets", func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupR3Capture, "target", json.RawMessage(backupR3OtherTarget))
				backupR3SetInputField(t, o, backupR3Capture, "target", json.RawMessage(backupR3OtherTarget))
			}, off},
			{"observed", backupR3Field(backupR3Capture, "observed_at", "yesterday"), off},
		})
}

func TestBackupR3ValidationRejection(t *testing.T) {
	const off = "validationRejected=false"
	result := func(check, outcome, observed string) map[string]any {
		return map[string]any{"check_id": check, "check_version": "1", "result": outcome, "observed_at": observed}
	}
	backupR3RunEvidence(t, false, backupR3Rejected(backupValidateStep), "validation=false; validationRejected=true", []backupR3EvidenceCase{
		{"pending", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupValidateStep) }, off},
		{"missing", func(_ *testing.T, o *store.OperationRecord) { backupR3RemoveStep(o, backupValidateStep) }, off},
		{"result", backupR3Field(backupValidateStep, "validation_result", "valid"), off},
		{"input backup", backupR3InputField(backupValidateStep, "backup_id", "other"), off},
		{"input environment", backupR3InputField(backupValidateStep, "environment_id", "other"), off},
		{"output backup", backupR3Field(backupValidateStep, "backup_id", "other"), off},
		{"output environment", backupR3Field(backupValidateStep, "environment_id", "other"), off},
		{"output generation", backupR3Field(backupValidateStep, "data_generation_id", "other"), off},
		{"observed", backupR3Field(backupValidateStep, "observed_at", "yesterday"), off},
		{"no results", backupR3Field(backupValidateStep, "check_results", []any{}), off},
		{"results not a list", backupR3Field(backupValidateStep, "check_results", "fail"), off},
		{"unknown result", backupR3Field(backupValidateStep, "check_results", []any{result("a", "fail", backupNow()), result("b", "unknown", backupNow())}), off},
		{"result observed", backupR3Field(backupValidateStep, "check_results", []any{result("a", "fail", "yesterday")}), off},
		{"no failure", backupR3Field(backupValidateStep, "check_results", []any{result("a", "pass", backupNow())}), off},
		{"single failure", backupR3Field(backupValidateStep, "check_results", []any{result("a", "fail", backupNow())}), ""},
	})
}
