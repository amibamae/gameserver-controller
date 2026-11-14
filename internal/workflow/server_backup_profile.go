package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func validateServerBackupProfile(server store.LogicalServer, requestedID string, profile ServerBackupProfile) error {
	if !backupProfileReleaseBound(requestedID, profile) || !backupProfileSetsBounded(profile) || !backupProfileStorageBound(server, profile) {
		return errors.New("trusted backup profile must bind a release, offline consistency, bounded data sets/checks, and the server storage profile")
	}
	if !backupProfileUnitBound(server, profile.UnitRef) {
		return errors.New("trusted backup profile unit reference does not bind the configured execution adapter and environment")
	}
	if err := validateBackupProfileDataSets(profile); err != nil {
		return err
	}
	return validateBackupProfileChecks(profile.ValidationChecks)
}

// backupProfileReleaseBound reports whether the profile is the requested one
// and names a bounded game release, name, version and optional world format.
func backupProfileReleaseBound(requestedID string, profile ServerBackupProfile) bool {
	if profile.ProfileID != requestedID || profile.ProfileID == "" {
		return false
	}
	return backupBoundedText(profile.GameReleaseID, 500) && backupBoundedText(profile.GameName, 200) &&
		backupBoundedText(profile.GameVersion, 200) && len(profile.WorldFormat) <= 200
}

// backupProfileSetsBounded reports whether the profile selects offline
// consistency with 1-128 data sets and 1-64 validation checks.
func backupProfileSetsBounded(profile ServerBackupProfile) bool {
	return profile.ConsistencyMode == "offline" &&
		backupCountInBounds(len(profile.DataSets), 128) &&
		backupCountInBounds(len(profile.ValidationChecks), 64)
}

func backupCountInBounds(count, limit int) bool {
	return count > 0 && count <= limit
}

// backupProfileStorageBound reports whether the profile names a storage
// adapter, the server's storage profile and a bounded container.
func backupProfileStorageBound(server store.LogicalServer, profile ServerBackupProfile) bool {
	return profile.StorageAdapterID != "" && profile.StorageProfileID == server.StorageProfile &&
		backupBoundedText(profile.StorageContainer, 500)
}

// backupBoundedText reports whether value is non-empty and at most limit bytes.
func backupBoundedText(value string, limit int) bool {
	return value != "" && len(value) <= limit
}

// backupProfileUnitBound reports whether the profile's unit reference is a
// systemd unit of the server's execution adapter and environment.
func backupProfileUnitBound(server store.LogicalServer, raw json.RawMessage) bool {
	var unitRef struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if err := json.Unmarshal(raw, &unitRef); err != nil || !isObject(raw) {
		return false
	}
	return unitRef.AdapterID == server.ExecutionAdapterID && unitRef.EnvironmentID == server.EnvironmentID &&
		unitRef.ResourceKind == "systemd-unit" && unitRef.ExternalID != ""
}

// validateBackupProfileDataSets checks each data set in order: a complete
// declaration, a unique ID, the selected consistency mode exactly once, then
// safe unique paths.
func validateBackupProfileDataSets(profile ServerBackupProfile) error {
	seenDataSets := make(map[string]struct{}, len(profile.DataSets))
	for _, dataSet := range profile.DataSets {
		if !backupDataSetDeclarationComplete(dataSet) {
			return fmt.Errorf("trusted backup profile contains an incomplete data-set declaration %q", dataSet.DataSetID)
		}
		if _, duplicate := seenDataSets[dataSet.DataSetID]; duplicate {
			return fmt.Errorf("trusted backup profile repeats data set %q", dataSet.DataSetID)
		}
		seenDataSets[dataSet.DataSetID] = struct{}{}
		if !containsString(dataSet.SupportedConsistencyModes, profile.ConsistencyMode) || !uniqueBackupStrings(dataSet.SupportedConsistencyModes) {
			return fmt.Errorf("trusted data set %q does not declare the selected consistency mode exactly once", dataSet.DataSetID)
		}
		if err := validateBackupDataSetPaths(dataSet); err != nil {
			return err
		}
	}
	return nil
}

// backupDataSetDeclarationComplete reports whether a data set has a bounded
// ID, a size limit within JSON's safe integer range, 1-128 relative paths and
// at least one consistency mode.
func backupDataSetDeclarationComplete(dataSet ServerBackupDataSet) bool {
	if !backupBoundedText(dataSet.DataSetID, 200) || dataSet.MaximumBytes < 1 || dataSet.MaximumBytes > 9007199254740991 {
		return false
	}
	return backupCountInBounds(len(dataSet.RelativePaths), 128) && len(dataSet.SupportedConsistencyModes) > 0
}

func validateBackupDataSetPaths(dataSet ServerBackupDataSet) error {
	paths := make(map[string]struct{}, len(dataSet.RelativePaths))
	for _, relative := range dataSet.RelativePaths {
		if !validBackupRelativePath(relative) {
			return fmt.Errorf("trusted data set %q contains an unsafe relative path", dataSet.DataSetID)
		}
		if _, duplicate := paths[relative]; duplicate {
			return fmt.Errorf("trusted data set %q repeats a path", dataSet.DataSetID)
		}
		paths[relative] = struct{}{}
	}
	return nil
}

// validateBackupProfileChecks checks each validation check in order for a
// bounded ID and version, then a unique ID.
func validateBackupProfileChecks(checks []ReadinessRequirement) error {
	seenChecks := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if !backupBoundedText(check.CheckID, 160) || !backupBoundedText(check.CheckVersion, 40) {
			return errors.New("trusted backup validation checks require bounded IDs and versions")
		}
		if _, duplicate := seenChecks[check.CheckID]; duplicate {
			return fmt.Errorf("trusted backup profile repeats validation check %q", check.CheckID)
		}
		seenChecks[check.CheckID] = struct{}{}
	}
	return nil
}

func validBackupRelativePath(value string) bool {
	if !backupBoundedText(value, 512) || strings.Contains(value, "\\") || path.IsAbs(value) {
		return false
	}
	if path.Clean(value) != value || value == "." {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func verifyBackupDataSetsDeclared(descriptor adapter.Descriptor, profile ServerBackupProfile) error {
	var described adapter.Capability
	for _, capability := range descriptor.Capabilities {
		if capability.Name == "game.data.describe" {
			described = capability
			break
		}
	}
	if described.Name == "" {
		return errors.New("game adapter does not declare game.data.describe")
	}
	encoded, err := json.Marshal(described.Constraints)
	if err != nil {
		return err
	}
	var constraints struct {
		PersistentDataSets []struct {
			GameReleaseID             string   `json:"game_release_id"`
			DataSetIDs                []string `json:"data_set_ids"`
			SupportedConsistencyModes []string `json:"supported_consistency_modes"`
		} `json:"persistent_data_sets"`
	}
	if err := json.Unmarshal(encoded, &constraints); err != nil {
		return fmt.Errorf("decode game adapter persistent-data declarations: %w", err)
	}
	expected := make([]string, 0, len(profile.DataSets))
	for _, dataSet := range profile.DataSets {
		expected = append(expected, dataSet.DataSetID)
	}
	for _, declared := range constraints.PersistentDataSets {
		if declared.GameReleaseID == profile.GameReleaseID && sameBackupStringSet(declared.DataSetIDs, expected) &&
			containsString(declared.SupportedConsistencyModes, profile.ConsistencyMode) {
			return nil
		}
	}
	return errors.New("trusted backup data sets or consistency mode do not match the game adapter release declaration")
}

func sameBackupStringSet(left, right []string) bool {
	if len(left) != len(right) || !uniqueBackupStrings(left) || !uniqueBackupStrings(right) {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func uniqueBackupStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func backupObjectKey(serverID, backupID, dataSetID string) string {
	return url.PathEscape(serverID) + "/" + url.PathEscape(backupID) + "/" + url.PathEscape(dataSetID)
}

func backupDataStepID(action, dataSetID string) string {
	digest := sha256.Sum256([]byte(dataSetID))
	return action + "-" + hex.EncodeToString(digest[:])
}

func backupCaptureID(operationID, dataSetID string) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + dataSetID))
	return "capture-" + hex.EncodeToString(digest[:])
}

func cloneStringAnyMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func (w *ServerBackupWorkflow) loadProfile(ctx context.Context, operation store.OperationRecord) (store.StoredLogicalServer, ServerBackupProfile, error) {
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, err
	}
	var parameters serverBackupParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, errors.New("decode accepted backup profile")
	}
	profile, err := w.profiles.ResolveServerBackup(ctx, stored.Record, parameters.BackupProfile)
	if err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, err
	}
	if err := validateServerBackupProfile(stored.Record, parameters.BackupProfile, profile); err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, err
	}
	if operation.RequestSnapshot.OperationType != "server.backup" || parameters.BackupProfile == "" {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, errors.New("accepted operation does not bind a backup profile")
	}
	return stored, profile, nil
}

func (w *ServerBackupWorkflow) dataSetForStep(ctx context.Context, operation store.OperationRecord, stepID, action string) (ServerBackupDataSet, bool) {
	_, profile, err := w.loadProfile(ctx, operation)
	if err != nil {
		return ServerBackupDataSet{}, false
	}
	plannedIDs := backupPlannedDataSetIDs(operation)
	for _, dataSet := range profile.DataSets {
		if backupDataStepID(action, dataSet.DataSetID) == stepID && containsString(plannedIDs, dataSet.DataSetID) {
			return dataSet, true
		}
	}
	return ServerBackupDataSet{}, false
}

func backupPlannedDataSetIDs(operation store.OperationRecord) []string {
	step := operationStep(operation, backupPrepareStep)
	var input struct {
		DataSetIDs []string `json:"data_set_ids"`
	}
	if json.Unmarshal(step.Input, &input) != nil {
		return nil
	}
	return input.DataSetIDs
}
