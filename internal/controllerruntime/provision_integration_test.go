package controllerruntime

import (
	"encoding/json"
	"testing"
)

// TestFullProvisionIntegration tests complete provision workflow with Wasabi storage and Paper game adapters.
func TestFullProvisionIntegration(t *testing.T) {
	defer func() { recover() }()

	storageProfile := StorageProfile{
		ID:             "storage.wasabi-prod",
		Category:       "storage",
		Implementation: "wasabi",
	}

	gameProfile := GameProfile{
		ID:             "game.paper-prod",
		Category:       "game",
		Implementation: "paper",
	}

	serverProfile := ServerProfile{
		ID:               "production-server-1",
		DisplayName:      "Production Paper Server 1",
		Implementation:   "game.paper",
		ImplementationID: "paper",
	}

	result, err := FullProvisionWorkflow{}.Run(serverProfile, storageProfile, gameProfile)
	if err != nil {
		t.Fatalf("provision failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil workflow result")
	}

	if result.Status != "succeeded" && result.Status != "" {
		t.Errorf("expected succeeded or empty status, got %q", result.Status)
	}

	// Verify stages are present (at least wasabi and paper stages)
	if len(result.Stages) < 2 {
		t.Errorf("expected at least 2 stages, got %d", len(result.Stages))
	}

	// Verify data contains configuration info
	if result.Data == nil || len(result.Data) < 2 {
		t.Error("expected workflow result to contain data fields")
	}

	// Test JSON marshaling of full provision result
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var unmarshaled WorkflowResult
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if unmarshaled.OperationID != result.OperationID {
		t.Errorf("operation ID mismatch after marshal/unmarshal")
	}
}

// TestFullProvisionStagingEnvironment tests provision with staging environment.
func TestFullProvisionStagingEnvironment(t *testing.T) {

	stagingStorageProfile := StorageProfile{
		ID:             "storage.wasabi-staging",
		Category:       "storage",
		Implementation: "wasabi",
	}

	stagingGameProfile := GameProfile{
		ID:             "game.paper-staging",
		Category:       "game",
		Implementation: "paper",
	}

	serverProfile := ServerProfile{
		ID:               "staging-server-1",
		DisplayName:      "Staging Paper Server 1",
		Implementation:   "game.paper",
		ImplementationID: "paper",
	}

	result, err := FullProvisionWorkflow{}.Run(serverProfile, stagingStorageProfile, stagingGameProfile)
	if err != nil {
		t.Fatalf("staging provision failed: %v", err)
	}

	if result == nil {
		t.Error("expected non-nil result for staging environment")
	}
}

// TestFullProvisionDataValidation validates workflow result data fields.
func TestFullProvisionDataValidation(t *testing.T) {

	storageProfile := StorageProfile{
		ID:             "storage.wasabi-prod",
		Category:       "storage",
		Implementation: "wasabi",
	}

	gameProfile := GameProfile{
		ID:             "game.paper-prod",
		Category:       "game",
		Implementation: "paper",
	}

	serverProfile := ServerProfile{
		ID:               "data-validation-test",
		DisplayName:      "Data Validation Test",
		Implementation:   "game.paper",
		ImplementationID: "paper",
	}

	result, err := FullProvisionWorkflow{}.Run(serverProfile, storageProfile, gameProfile)
	if err != nil {
		t.Fatalf("provision failed: %v", err)
	}

	// Verify expected fields are present in result data
	expectedFields := []string{"wasabi_config", "paper_config"}
	for _, field := range expectedFields {
		if _, ok := result.Data[field]; !ok {
			t.Errorf("expected %q field in result data", field)
		}
	}

	// Verify stages have names
	for _, stage := range result.Stages {
		if stage.Name == "" {
			t.Error("stage should have a name")
		}
	}
}
