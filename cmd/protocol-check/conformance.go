package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

func (pkg *protocolPackage) runConformance() error {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var failures []string
	for _, path := range paths {
		fixture, err := parseJSONFile(path)
		if err == nil {
			var actual map[string]any
			actual, err = evaluate(pkg, asObject(fixture))
			if err == nil {
				var encoded []byte
				encoded, err = json.Marshal(actual)
				if err == nil {
					var normalized any
					normalized, err = parseJSON(encoded, filepath.Base(path)+" result")
					if err == nil {
						err = compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$")
					}
				}
			}
		}
		if err != nil {
			failures = append(failures, filepath.Base(path)+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("conformance failed:\n - %s", strings.Join(failures, "\n - "))
	}
	return nil
}

// fixtureEvaluator computes a fixture's result from its input.
type fixtureEvaluator func(*protocolPackage, map[string]any) (map[string]any, error)

// fixtureEvaluators maps every fixture_id to its reference evaluator. It is
// filled in init because some evaluators call evaluate themselves.
var fixtureEvaluators map[string]fixtureEvaluator

func init() {
	fixtureEvaluators = map[string]fixtureEvaluator{
		"protocol.json.raw-wire-interoperability":                            withoutPackage(evaluateRawJSONInteroperability),
		"execution.unit.restart.clean-stop-distinct-runtime":                 evaluateUnitRestart,
		"execution.unit.create.exact-definition-no-autostart":                evaluateUnitCreate,
		"hosting.instance.find-by-resource-key.create-recovery":              evaluateInstanceCreateRecovery,
		"game.release.describe.integrity-and-identity":                       evaluateGameReleaseDescription,
		"game.install.release-binding-and-reconciliation":                    evaluateGameInstallContract,
		"game.backup.capture.binding-and-stream-integrity":                   evaluateBackupCapture,
		"operations.type-binding":                                            evaluateOperationTypeBinding,
		"operations.unknown-result-reference-projection":                     evaluateUnknownResultReferenceProjection,
		"operations.request-fingerprint-jcs":                                 withoutPackage(evaluateRequestFingerprintJCS),
		"integration.proxy.backend.drain-unregister-state-confirmation":      evaluateProxyBackendDrainUnregisterState,
		"integration.proxy.inspect.target-state-and-routability":             evaluateProxyBackendInspect,
		"integration.proxy.players.inspect.fresh-count-and-target-binding":   evaluateProxyBackendPlayersInspect,
		"integration.proxy.cohort.inspect.binding-and-freshness":             evaluateProxyCohortInspect,
		"integration.dns.record.upsert.purpose-readiness-and-confirmation":   evaluateDNSRecordUpsert,
		"hosting.bootstrap.host-key-handoff":                                 evaluateHostKeyHandoff,
		"integration.proxy.cohort.automatic-move":                            evaluateProxyAutomaticMove,
		"integration.network-profile.requirements":                           evaluateNetworkProfileRequirements,
		"integration.notification.proxy-outage-transitions":                  evaluateProxyOutageTransitions,
		"integration.notification.send.outcome-and-privacy":                  evaluateNotificationSend,
		"game.configure.pinned-schema-and-result-binding":                    evaluateConfiguration,
		"lifecycle.reject.unlisted-transition":                               evaluateLifecycleRejectUnlistedTransition,
		"lifecycle.replacement.identity":                                     evaluateLifecycleReplacementIdentity,
		"lifecycle.required-readiness-set":                                   evaluateLifecycleRequiredReadinessSet,
		"compatibility.protocol-version-negotiation":                         evaluateCompatibilityProtocolVersionNegotiation,
		"compatibility.response-evolution":                                   evaluateCompatibilityResponseEvolution,
		"compatibility.operation-upgrade-recovery":                           evaluateCompatibilityOperationUpgradeRecovery,
		"transport.storage.profile-negotiated":                               evaluateTransportStorageFixtures,
		"transport.storage.profile-not-selected":                             evaluateTransportStorageFixtures,
		"transport.storage.selection-not-offered":                            evaluateTransportStorageFixtures,
		"management.operations.submit-decision":                              evaluateManagementSubmitDecision,
		"management.pagination.cursor-scope":                                 evaluateManagementPaginationCursorScope,
		"management.authentication.credential-profiles":                      evaluateManagementAuthenticationCredentialProfiles,
		"management.authentication.actor-spoofing":                           evaluateManagementAuthenticationActorSpoofing,
		"operations.authentication.client-session-expiry":                    evaluateOperationsAuthenticationClientSessionExpiry,
		"operations.cancellation.before-dispatch":                            evaluateOperationsCancellationFixtures,
		"operations.cancellation.unknown-after-dispatch":                     evaluateOperationsCancellationFixtures,
		"operations.reconcile.unknown-create-outcome":                        evaluateOperationsReconcileUnknownCreateOutcome,
		"safety.destroy.blocked-stale-data-generation":                       evaluateSafetyDestroyFixtures,
		"safety.destroy.blocked-unverified-backup":                           evaluateSafetyDestroyFixtures,
		"security.staging.cannot-write-production-backup":                    evaluateSecurityStagingCannotWriteProductionBackup,
		"security.staging.production-write-scope-denied":                     evaluateSecurityStagingCannotWriteProductionBackup,
		"security.production.backup-write-authorized":                        evaluateSecurityStagingCannotWriteProductionBackup,
		"hosting.instance.wait.pending-remains-pending":                      evaluateHostingInstanceWaitFixtures,
		"hosting.instance.wait.succeeded-postcondition-confirmed":            evaluateHostingInstanceWaitFixtures,
		"hosting.instance.wait.succeeded-state-mismatch":                     evaluateHostingInstanceWaitFixtures,
		"hosting.instance.wait.unknown-requires-inspection":                  evaluateHostingInstanceWaitFixtures,
		"hosting.instance.wait.operation-reference-mismatch":                 evaluateHostingInstanceWaitFixtures,
		"hosting.instance.wait.failed-terminal-requires-inspection":          evaluateHostingInstanceWaitFixtures,
		"hosting.instance.wait.without-operation-reference":                  evaluateHostingInstanceWaitFixtures,
		"hosting.instance.destroy.approval-exact-scope":                      evaluateHostingInstanceDestroyFixtures,
		"hosting.instance.destroy.approval-wrong-action":                     evaluateHostingInstanceDestroyFixtures,
		"hosting.instance.destroy.approval-target-mismatch":                  evaluateHostingInstanceDestroyFixtures,
		"hosting.instance.destroy.approval-environment-mismatch":             evaluateHostingInstanceDestroyFixtures,
		"hosting.instance.destroy.approval-plan-mismatch":                    evaluateHostingInstanceDestroyFixtures,
		"hosting.instance.destroy.approval-expired":                          evaluateHostingInstanceDestroyFixtures,
		"hosting.instance.destroy.approval-missing-approver":                 evaluateHostingInstanceDestroyFixtures,
		"hosting.instance.destroy.authorization-required":                    evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.destroy.verified-backup-required":                  evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.destroy.current-generation-backup-required":        evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.destroy.stopped-required":                          evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.destroy.completed-destroyed":                       evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.destroy.accepted-not-destroyed":                    evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.destroy.unknown-requires-inspection":               evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.destroy.target-mismatch":                           evaluateHostingInstanceDestroyAuthorizationRequired,
		"hosting.instance.reboot.core-missing-save-blocked":                  evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-save-bound":                            evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-clean-stop-bound":                      evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-save-mismatch-blocked":                 evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-unknown-data-blocked":                  evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-emergency-approved":                    evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-emergency-target-mismatch":             evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-emergency-wrong-action":                evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.core-no-data-no-gate":                       evaluateHostingInstanceRebootFixtures,
		"hosting.instance.reboot.completed-active":                           evaluateHostingInstanceRebootCompletedActive,
		"hosting.instance.reboot.accepted-not-complete":                      evaluateHostingInstanceRebootCompletedActive,
		"hosting.instance.reboot.unknown-requires-inspection":                evaluateHostingInstanceRebootCompletedActive,
		"hosting.instance.reboot.target-mismatch":                            evaluateHostingInstanceRebootCompletedActive,
		"hosting.instance.start.completed-active":                            evaluateHostingInstanceFixtures,
		"hosting.instance.start.accepted-not-active":                         evaluateHostingInstanceFixtures,
		"hosting.instance.start.unknown-requires-inspection":                 evaluateHostingInstanceFixtures,
		"hosting.instance.start.target-mismatch":                             evaluateHostingInstanceFixtures,
		"hosting.instance.stop.completed-stopped":                            evaluateHostingInstanceFixtures,
		"hosting.instance.stop.accepted-not-stopped":                         evaluateHostingInstanceFixtures,
		"hosting.instance.stop.unknown-requires-inspection":                  evaluateHostingInstanceFixtures,
		"hosting.instance.stop.target-mismatch":                              evaluateHostingInstanceFixtures,
		"hosting.regions.list.filtered":                                      evaluateHostingRegionsListFiltered,
		"hosting.plans.list.resource-filter":                                 evaluateHostingRegionsListFiltered,
		"hosting.plans.list.minimum-filter-mismatch":                         evaluateHostingRegionsListFiltered,
		"hosting.os_offerings.list.unknown-not-available":                    evaluateHostingRegionsListFiltered,
		"hosting.instance.inspect.exact-active":                              evaluateHostingInstanceInspectFixtures,
		"hosting.instance.inspect.unknown-not-active":                        evaluateHostingInstanceInspectFixtures,
		"hosting.instance.inspect.target-mismatch":                           evaluateHostingInstanceInspectFixtures,
		"execution.unit.inspect.exact-target-state":                          evaluateSystemdUnitInspection,
		"execution.unit.start.accepted-not-ready":                            evaluateExecutionUnitFixtures,
		"execution.unit.start.unknown-requires-inspection":                   evaluateExecutionUnitFixtures,
		"execution.unit.start.target-mismatch":                               evaluateExecutionUnitFixtures,
		"execution.unit.graceful-stop.completed-stopped":                     evaluateExecutionUnitFixtures,
		"execution.unit.graceful-stop.unknown-requires-inspection":           evaluateExecutionUnitFixtures,
		"execution.unit.force-stop.approval-required":                        evaluateExecutionUnitForceStopFixtures,
		"execution.unit.force-stop.approval-scope-denied":                    evaluateExecutionUnitForceStopFixtures,
		"execution.unit.force-stop.completed-stopped":                        evaluateExecutionUnitForceStopFixtures,
		"execution.unit.force-stop.accepted-not-stopped":                     evaluateExecutionUnitForceStopFixtures,
		"execution.unit.force-stop.target-mismatch":                          evaluateExecutionUnitForceStopFixtures,
		"execution.unit.force-stop.unknown-requires-inspection":              evaluateExecutionUnitForceStopFixtures,
		"game.migrate.approved-copy-success":                                 evaluateGameMigrateFixtures,
		"game.migrate.approval-required":                                     evaluateGameMigrateFixtures,
		"game.migrate.verified-backup-required":                              evaluateGameMigrateFixtures,
		"game.migrate.distinct-target-required":                              evaluateGameMigrateFixtures,
		"game.migrate.target-binding":                                        evaluateGameMigrateFixtures,
		"game.migrate.complete-checks-required":                              evaluateGameMigrateFixtures,
		"game.migrate.compatibility-required":                                evaluateGameMigrateFixtures,
		"game.migrate.backup-result-binding":                                 evaluateGameMigrateFixtures,
		"game.migrate.target-stopped":                                        evaluateGameMigrateFixtures,
		"game.migrate.unknown-requires-reconciliation":                       evaluateGameMigrateFixtures,
		"game.compatibility.check.compatible":                                evaluateGameCompatibilityCheckFixtures,
		"game.compatibility.check.migration-required":                        evaluateGameCompatibilityCheckFixtures,
		"game.compatibility.check.incompatible":                              evaluateGameCompatibilityCheckFixtures,
		"game.compatibility.check.unknown":                                   evaluateGameCompatibilityCheckFixtures,
		"game.compatibility.check.incomplete":                                evaluateGameCompatibilityCheckFixtures,
		"game.compatibility.check.unknown-reported-compatible":               evaluateGameCompatibilityCheckFixtures,
		"game.compatibility.check.target-mismatch":                           evaluateGameCompatibilityCheckFixtures,
		"game.health.check.unknown-not-healthy":                              evaluateGameHealthCheckUnknownNotHealthy,
		"game.backup.validate.exact-objects":                                 evaluateGameBackupValidateFixtures,
		"game.backup.validate.unknown-not-valid":                             evaluateGameBackupValidateFixtures,
		"game.data.save.backup-prepare.binding":                              evaluateGameDataSaveBackupPrepareFixtures,
		"game.data.save.backup-prepare.reject-unsafe-datasets":               evaluateGameDataSaveBackupPrepareFixtures,
		"game.save.unknown-requires-reconciliation":                          evaluateGameSaveUnknownRequiresReconciliation,
		"operations.backup.no-resume-final-state":                            evaluateBackupNoResumePolicy,
		"operations.backup.initial-state-resume-policy":                      evaluateBackupInitialStatePolicy,
		"operations.restore.final-state-policy":                              evaluateRestoreFinalStatePolicy,
		"operations.typed-provision-backup-restore-destroy-results":          evaluateOperationsTypedProvisionBackupRestoreDestroyResults,
		"operations.typed-provision-backup-restore-destroy-target-mismatch":  evaluateOperationsTypedProvisionBackupRestoreDestroyResults,
		"operations.typed-provision-backup-restore-readiness-binding":        evaluateOperationsTypedProvisionBackupRestoreDestroyResults,
		"operations.typed-provision-backup-restore-readiness-freshness":      evaluateOperationsTypedProvisionBackupRestoreDestroyResults,
		"operations.typed-restore-data-generation-not-rotated":               evaluateOperationsTypedProvisionBackupRestoreDestroyResults,
		"operations.typed-define-start-stop-results":                         evaluateOperationsTypedDefineStartStopResults,
		"operations.typed-define-start-stop-target-mismatch":                 evaluateOperationsTypedDefineStartStopResults,
		"operations.typed-define-start-stop-future-readiness":                evaluateOperationsTypedDefineStartStopResults,
		"operations.typed-define-start-stop-no-drain":                        evaluateOperationsTypedDefineStartStopResults,
		"operations.typed-define-start-stop-no-proxy-drain":                  evaluateOperationsTypedDefineStartStopResults,
		"execution.logs.health.delete.safety":                                evaluateExecutionLogsHealthDeleteSafety,
		"execution.unit.health.diagnostic-redaction-and-binding":             evaluateExecutionHealthDiagnostics,
		"execution.unit.logs.byte-bound-and-redaction":                       evaluateExecutionLogRead,
		"integration.proxy.register.expired-readiness-rejected":              evaluateIntegrationProxyRegisterExpiredReadinessRejected,
		"integration.proxy.register.accepted-is-not-routable":                evaluateIntegrationProxyRegisterAcceptedIsNotRoutable,
		"integration.proxy.unknown-drain-and-unregister-require-inspection":  evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection,
		"game.release.install.configure.fail-closed":                         evaluateGameReleaseInstallConfigureFailClosed,
		"game.install.repeat-same-release-is-idempotent":                     evaluateGameInstallFixtures,
		"game.install.completed-verifies-exact-artifacts":                    evaluateGameInstallFixtures,
		"game.install.incomplete-artifact-set-requires-reconciliation":       evaluateGameInstallFixtures,
		"game.validate-installation.unknown-blocks-start":                    evaluateGameValidateInstallationFixtures,
		"game.validate-installation.valid-exact-artifacts":                   evaluateGameValidateInstallationFixtures,
		"game.validate-installation.valid-incomplete-artifacts-blocks-start": evaluateGameValidateInstallationFixtures,
		"game.validate-installation.valid-claim-with-unverified-artifact":    evaluateGameValidateInstallationFixtures,
		"game.validate-installation.valid-claim-with-checksum-mismatch":      evaluateGameValidateInstallationFixtures,
		"game.readiness.unknown-required-check-blocks-ready":                 evaluateGameReadinessUnknownRequiredCheckBlocksReady,
		"readiness-required-before-transfer":                                 evaluateReadinessRequiredBeforeTransfer,
		"readiness-target-mismatch-before-transfer":                          evaluateReadinessRequiredBeforeTransfer,
		"readiness-server-mismatch-before-transfer":                          evaluateReadinessRequiredBeforeTransfer,
		"velocity-transfer-outcome-unknown-reconciliation":                   evaluateVelocityTransferUnknownReconciliation,
		"velocity-transfer-privacy-canaries":                                 evaluateVelocityTransferPrivacy,
		"velocity-transfer-partial-results":                                  evaluateVelocityTransferPartialResults,
		"velocity-transfer-count-mismatch":                                   evaluateVelocityTransferPartialResults,
		"velocity-transfer-context-mismatch":                                 evaluateVelocityTransferPartialResults,
		"velocity-transfer-duplicate-session-result":                         evaluateVelocityTransferPartialResults,
		"velocity-transfer-target-mismatch":                                  evaluateVelocityTransferPartialResults,
		"storage.backup.partial-upload-not-verified":                         evaluateStorageBackupPartialUploadNotVerified,
		"storage.object.checksum-mismatch-blocks-restore":                    evaluateStorageObjectChecksumMismatchBlocksRestore,
		"storage.object.etag-not-checksum":                                   evaluateStorageObjectEtagNotChecksum,
		"storage.staging.cannot-delete-production-object":                    evaluateStorageStagingCannotDeleteProductionObject,
		"storage.staging.production-delete-scope-denied":                     evaluateStorageStagingCannotDeleteProductionObject,
		"storage.production.backup-delete-authorized":                        evaluateStorageStagingCannotDeleteProductionObject,
		"operations.idempotency.same-key-same-request":                       evaluateOperationsIdempotencySameKeySameRequest,
		"operations.idempotency.same-key-different-request":                  evaluateOperationsIdempotencySameKeyDifferentRequest,
		"operations.idempotency.expired-key-reuse":                           evaluateOperationsIdempotencyExpiredKeyReuse,
		"operations.recovery.restart-preserves-lock":                         evaluateOperationsRecoveryRestartPreservesLock,
		"operations.events.duplicate-delivery":                               evaluateOperationsEventsDuplicateDelivery,
		"operations.events.gap-recovery":                                     evaluateOperationsEventsGapRecovery,
		"operations.events.monotonic-sequence":                               evaluateOperationsEventsMonotonicSequence,
		"hosting.provision.valid.standard-os":                                evaluateHostingProvisionValidStandardOs,
		"hosting.bootstrap.execution-binding-and-readiness-gate":             evaluateHostingBootstrapExecutionBindingAndReadinessGate,
		"hosting.bootstrap.rejects-false-success":                            evaluateHostingBootstrapRejectsFalseSuccess,
	}
}

func withoutPackage(evaluator func(map[string]any) (map[string]any, error)) fixtureEvaluator {
	return func(_ *protocolPackage, fixture map[string]any) (map[string]any, error) { return evaluator(fixture) }
}

func evaluate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	fixtureID := stringValue(fixture, "fixture_id")
	evaluator, ok := fixtureEvaluators[fixtureID]
	if !ok {
		return nil, fmt.Errorf("no evaluator registered for fixture %q", fixtureID)
	}
	return evaluator(pkg, fixture)
}

func evaluateVelocityTransferUnknownReconciliation(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	request := asObject(input["transfer_request"])
	result := asObject(input["transfer_result"])
	transferState, err := evaluate(pkg, map[string]any{
		"fixture_id": "velocity-transfer-partial-results",
		"input": map[string]any{
			"transfer_request": request,
			"transfer_result":  result,
			"validation_time":  input["transfer_validation_time"],
		},
	})
	if err != nil {
		return nil, err
	}
	transferValid := boolValue(transferState, "result_valid_and_bound")
	unknownSessions := make(map[string]bool)
	for _, raw := range arrayValue(result, "results") {
		item := asObject(raw)
		if stringValue(item, "outcome") == "outcome_unknown" {
			unknownSessions[stringValue(item, "proxy_session_id")] = true
		}
	}

	caseResults := make([]any, 0, len(arrayValue(input, "reconciliation_cases")))
	for _, rawCase := range arrayValue(input, "reconciliation_cases") {
		reconciliation := asObject(rawCase)
		inspectInput := asObject(reconciliation["inspection_request"])
		inspectOutput := asObject(reconciliation["inspection_result"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-session-inspect-input", inspectInput); err != nil {
			return nil, fmt.Errorf("session inspect request does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-session-inspect-output", inspectOutput); err != nil {
			return nil, fmt.Errorf("session inspect result does not match schema: %w", err)
		}
		decisionTimeText := stringValue(reconciliation, "decision_time")
		decisionTime, err := parseTime(decisionTimeText)
		if err != nil {
			return nil, err
		}
		observedAt, err := parseTime(stringValue(inspectOutput, "observed_at"))
		if err != nil {
			return nil, err
		}
		maximumAge, err := integerValue(inspectInput["maximum_observation_age_seconds"])
		if err != nil {
			return nil, err
		}
		age := decisionTime.Sub(observedAt)
		fresh := age >= 0 && age <= time.Duration(maximumAge)*time.Second
		proxyRef := asObject(inspectInput["proxy_instance_ref"])
		sessionID := stringValue(inspectInput, "proxy_session_id")
		bound := reflect.DeepEqual(inspectInput["proxy_instance_ref"], inspectOutput["proxy_instance_ref"]) &&
			sessionID == stringValue(inspectOutput, "proxy_session_id") &&
			unknownSessions[sessionID] &&
			stringValue(proxyRef, "adapter_id") == "integration.velocity" &&
			stringValue(proxyRef, "environment_id") == stringValue(request, "environment_id") &&
			stringValue(proxyRef, "resource_kind") == "proxy-instance"

		retryAssertion := asObject(reconciliation["retry_readiness_assertion"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:readiness-assertion", retryAssertion); err != nil {
			return nil, fmt.Errorf("retry readiness assertion does not match schema: %w", err)
		}
		retryReadinessCurrent, err := readinessAssertionBindsAtCompletion(
			retryAssertion,
			decisionTimeText,
			stringValue(request, "server_id"),
			stringValue(request, "environment_id"),
			request["expected_instance_ref"],
		)
		if err != nil {
			return nil, err
		}

		state := "unresolved"
		if transferValid && bound && fresh {
			switch stringValue(inspectOutput, "connection_state") {
			case "connected":
				switch stringValue(inspectOutput, "current_backend_id") {
				case stringValue(request, "source_backend_id"):
					state = "still_on_source"
				case stringValue(request, "destination_backend_id"):
					state = "already_at_destination"
				default:
					state = "other_backend"
				}
			case "disconnected":
				state = "disconnected"
			case "unknown":
				state = "unknown"
			}
		}
		retryPermitted := state == "still_on_source" && retryReadinessCurrent
		caseResults = append(caseResults, map[string]any{
			"case_id":                         stringValue(reconciliation, "case_id"),
			"session_inspection_schema_valid": true,
			"session_inspection_bound":        bound,
			"session_inspection_fresh":        fresh,
			"retry_readiness_current":         retryReadinessCurrent,
			"reconciliation_state":            state,
			"retry_permitted":                 retryPermitted,
		})
	}
	return map[string]any{
		"transfer_result_valid_and_bound": transferValid,
		"reconciliation_cases":            caseResults,
	}, nil
}

func readinessAssertionBindsAtCompletion(assertion map[string]any, completedAt, serverID, environmentID string, instanceRef any) (bool, error) {
	if len(assertion) == 0 {
		return true, nil
	}
	completed, err := parseTime(completedAt)
	if err != nil {
		return false, err
	}
	observed, err := parseTime(stringValue(assertion, "observed_at"))
	if err != nil {
		return false, err
	}
	expires, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return false, err
	}
	assertedInstance := asObject(assertion["instance_ref"])
	bound := stringValue(assertion, "server_id") == serverID &&
		stringValue(assertion, "environment_id") == environmentID &&
		stringValue(assertedInstance, "environment_id") == environmentID &&
		reflect.DeepEqual(instanceRef, assertion["instance_ref"]) &&
		!completed.Before(observed) && completed.Before(expires)
	return bound, nil
}

func sameObjectRefSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet, rightSet := make(map[string]bool, len(left)), make(map[string]bool, len(right))
	for _, value := range left {
		encoded, err := json.Marshal(value)
		if err != nil || leftSet[string(encoded)] {
			return false
		}
		leftSet[string(encoded)] = true
	}
	for _, value := range right {
		encoded, err := json.Marshal(value)
		if err != nil || rightSet[string(encoded)] {
			return false
		}
		rightSet[string(encoded)] = true
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for key := range leftSet {
		if !rightSet[key] {
			return false
		}
	}
	return true
}

func sameStringSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]bool, len(left))
	for _, value := range left {
		str, ok := value.(string)
		if !ok {
			return false
		}
		set[str] = true
	}
	for _, value := range right {
		str, ok := value.(string)
		if !ok || !set[str] {
			return false
		}
	}
	return true
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func compareExpected(actual, expected any, path string) error {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualMap := asObject(actual)
		if actualMap == nil {
			return fmt.Errorf("%s: expected an object", path)
		}
		keys := make([]string, 0, len(expectedValue))
		for key := range expectedValue {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := actualMap[key]
			if !ok {
				return fmt.Errorf("%s.%s: missing expected value", path, key)
			}
			if err := compareExpected(value, expectedValue[key], path+"."+key); err != nil {
				return err
			}
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("%s: expected %v, got %v", path, expected, actual)
		}
	}
	return nil
}
