package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func containsProtocol(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func equalJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return string(left) == string(right)
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func adapterRequestID(operationID, stepID string, attempt int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", operationID, stepID, attempt)))
	return "step-" + hex.EncodeToString(sum[:16])
}

func protocolError(code, category, message, operationID string, retryable bool, recoveryAction string, operatorRequired bool) json.RawMessage {
	value := map[string]any{
		"code": code, "category": category, "message": message, "retryable": retryable,
		"operation_id": operationID,
		"recovery":     map[string]any{"action": recoveryAction, "operator_required": operatorRequired},
	}
	// A map of strings and booleans always encodes, so Marshal cannot fail.
	encoded, _ := json.Marshal(value)
	return encoded
}

func validProtocolError(schemas schemaRuntime, value json.RawMessage) bool {
	return isObject(value) && schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":common:protocol-error", value) == nil
}

func validOperationResult(schemas schemaRuntime, record store.OperationRecord, value json.RawMessage, success bool) bool {
	if !isObject(value) {
		return false
	}
	var header struct {
		OperationType string `json:"operation_type"`
	}
	if json.Unmarshal(value, &header) != nil || header.OperationType != record.OperationType {
		return false
	}
	schemaID := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":operations:operation-result"
	if success {
		typedID := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":operations:" + strings.ReplaceAll(record.OperationType, ".", "-") + "-result"
		if schemas.HasSchema(typedID) {
			schemaID = typedID
		}
	}
	return schemas.ValidateJSON(schemaID, value) == nil
}

func isObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func stringPointer(value string) *string { return &value }
