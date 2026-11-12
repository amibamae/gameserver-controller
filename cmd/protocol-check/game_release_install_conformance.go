package main

import (
	"encoding/hex"
	"reflect"
)

const (
	gameReleaseInputSchemaID  = "urn:gameserver-controller:0.21.0-draft:game:release-describe-input"
	gameReleaseOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:game:release-describe-output"
	gameInstallInputSchemaID  = "urn:gameserver-controller:0.21.0-draft:game:install-input"
	gameInstallOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:game:install-output"
)

func evaluateGameReleaseDescription(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	evaluations := make([]any, 0, len(arrayValue(asObject(fixture["input"]), "evaluations")))
	for _, rawEvaluation := range arrayValue(asObject(fixture["input"]), "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		requestValid := pkg.validateSchema(gameReleaseInputSchemaID, request) == nil
		responseValid := pkg.validateSchema(gameReleaseOutputSchemaID, response) == nil
		releaseBound := stringValue(request, "release_id") != "" &&
			stringValue(request, "release_id") == stringValue(response, "release_id")

		artifacts := arrayValue(response, "artifacts")
		artifactIDs := make([]any, 0, len(artifacts))
		checksumsValid := len(artifacts) > 0
		for _, rawArtifact := range artifacts {
			artifact := asObject(rawArtifact)
			artifactIDs = append(artifactIDs, artifact["artifact_id"])
			checksum := asObject(artifact["checksum"])
			if !validSHA256Checksum(checksum) {
				checksumsValid = false
			}
		}
		artifactIDsUnique := uniqueStrings(artifactIDs)
		accepted := requestValid && responseValid && releaseBound && artifactIDsUnique && checksumsValid
		evaluations = append(evaluations, map[string]any{
			"case_id":                      stringValue(evaluation, "case_id"),
			"request_schema_valid":         requestValid,
			"response_schema_valid":        responseValid,
			"release_id_bound":             releaseBound,
			"artifact_ids_unique":          artifactIDsUnique,
			"artifact_checksums_valid":     checksumsValid,
			"release_description_accepted": accepted,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func evaluateGameInstallContract(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	release := asObject(input["release_description"])
	releaseValid := pkg.validateSchema(gameReleaseOutputSchemaID, release) == nil
	releaseArtifactSetValid := len(arrayValue(release, "artifacts")) > 0

	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		requestValid := pkg.validateSchema(gameInstallInputSchemaID, request) == nil
		responseValid := pkg.validateSchema(gameInstallOutputSchemaID, response) == nil

		requestTarget := asObject(request["target"])
		instanceRef := asObject(requestTarget["instance_ref"])
		targetBound := reflect.DeepEqual(request["target"], response["target"]) &&
			stringValue(requestTarget, "environment_id") != "" &&
			stringValue(requestTarget, "environment_id") == stringValue(instanceRef, "environment_id")
		releaseBound := stringValue(request, "release_id") != "" &&
			stringValue(request, "release_id") == stringValue(response, "release_id") &&
			stringValue(request, "release_id") == stringValue(release, "release_id")

		artifactSetComplete, artifactChecksumsVerified := compareArtifactVerifications(
			arrayValue(release, "artifacts"), arrayValue(response, "artifact_results"),
		)
		artifactSetComplete = artifactSetComplete && releaseArtifactSetValid
		artifactChecksumsVerified = artifactChecksumsVerified && releaseArtifactSetValid
		outcome := stringValue(response, "outcome")
		verification := stringValue(response, "artifact_verification")
		started := boolValue(response, "installation_started")
		outcomeConsistent := outcome == "installed" && started && verification == "verified" ||
			outcome == "already_installed" && !started && verification == "verified"
		accepted := releaseValid && requestValid && responseValid && targetBound && releaseBound &&
			artifactSetComplete && artifactChecksumsVerified && outcomeConsistent
		reconciliationRequired := outcome == "outcome_unknown" ||
			(started && !accepted) ||
			((outcome == "installed" || outcome == "already_installed") && !accepted)
		evaluations = append(evaluations, map[string]any{
			"case_id":                              stringValue(evaluation, "case_id"),
			"release_schema_valid":                 releaseValid,
			"request_schema_valid":                 requestValid,
			"response_schema_valid":                responseValid,
			"target_bound":                         targetBound,
			"release_bound":                        releaseBound,
			"artifact_set_complete":                artifactSetComplete,
			"artifact_checksums_verified":          artifactChecksumsVerified,
			"installation_result_accepted":         accepted,
			"reconciliation_required_before_retry": reconciliationRequired,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func compareArtifactVerifications(required, results []any) (bool, bool) {
	requiredChecksums := make(map[string]any, len(required))
	requiredIDs := make([]any, 0, len(required))
	for _, rawArtifact := range required {
		artifact := asObject(rawArtifact)
		id := stringValue(artifact, "artifact_id")
		if id == "" {
			return false, false
		}
		requiredIDs = append(requiredIDs, id)
		requiredChecksums[id] = artifact["checksum"]
	}
	resultIDs := make([]any, 0, len(results))
	for _, rawResult := range results {
		resultIDs = append(resultIDs, asObject(rawResult)["artifact_id"])
	}
	complete := len(required) > 0 && uniqueStrings(requiredIDs) && uniqueStrings(resultIDs) &&
		sameStringSet(requiredIDs, resultIDs)
	checksumsVerified := complete
	for _, rawResult := range results {
		result := asObject(rawResult)
		id := stringValue(result, "artifact_id")
		pinnedChecksum, exists := requiredChecksums[id]
		if !exists || stringValue(result, "result") != "verified" ||
			!reflect.DeepEqual(result["expected_checksum"], pinnedChecksum) ||
			!reflect.DeepEqual(result["observed_checksum"], pinnedChecksum) {
			checksumsVerified = false
		}
	}
	return complete, checksumsVerified
}

func validSHA256Checksum(checksum map[string]any) bool {
	if stringValue(checksum, "algorithm") != "sha256" {
		return false
	}
	digest := stringValue(checksum, "digest")
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func init() {
	registerEvaluator(evaluateGameReleaseDescription, "game.release.describe.integrity-and-identity")
	registerEvaluator(evaluateGameInstallContract, "game.install.release-binding-and-reconciliation")
}
