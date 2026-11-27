// Package controllerruntime types for server recovery operations.
package controllerruntime

import (
	"encoding/json"
	"time"
)

// ServerRecoverParams defines the input for a server.recover operation.
type ServerRecoverParams struct {
	ServerID           string               `json:"server_id"`
	EnvironmentID      string               `json:"environment_id"`
	RecoveryIntent     string               `json:"recovery_intent"`
	TargetInstanceRef  *ExternalResourceRef `json:"target_instance_ref,omitempty"`
	TargetGenerationID *string              `json:"target_generation_id,omitempty"`
	BackupRef          *string              `json:"backup_ref,omitempty"`
	RecoveryBudget     *ServerRecoverBudget `json:"recovery_budget,omitempty"`
	LockScope          string               `json:"lock_scope,omitempty"`
	ForceRecovery      bool                 `json:"force_recovery,omitempty"`
	AuthorizationRef   *string              `json:"authorization_ref,omitempty"`
}

// ServerRecoverBudget defines the time budget for recovery.
type ServerRecoverBudget struct {
	MaxDuration int  `json:"max_duration"`
	RetryCount  *int `json:"retry_count,omitempty"`
}

// ExternalResourceRef is an external resource reference.
type ExternalResourceRef struct {
	Type        string   `json:"type"`
	ID          string   `json:"id"`
	Environment string   `json:"environment,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	State       string   `json:"state,omitempty"`
	ObservedAt  *string  `json:"observed_at,omitempty"`
	Scope       []string `json:"scope,omitempty"`
}

// ServerRecoverResult defines the output of a server.recover operation.
type ServerRecoverResult struct {
	RecoveryOutcome      string            `json:"recovery_outcome"`
	NextAction           string            `json:"next_action,omitempty"`
	OperationID          string            `json:"operation_id"`
	RecoveredAt          string            `json:"recovered_at,omitempty"`
	RecoverySteps        []RecoveryStep    `json:"recovery_steps,omitempty"`
	Warnings             []RecoveryWarning `json:"warnings,omitempty"`
	ExternalOperationRef *string           `json:"external_operation_ref,omitempty"`
}

// RecoveryStep records a step performed during recovery.
type RecoveryStep struct {
	Step    string `json:"step"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// RecoveryWarning records a warning about the recovery outcome.
type RecoveryWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ServerRecoverParamsJSON validates and unmarshals recover parameters from JSON.
func (p *ServerRecoverParams) UnmarshalJSON(data []byte) error {
	type Alias ServerRecoverParams
	aux := &struct {
		RecoveryBudget *RecoverBudgetAlias `json:"recovery_budget,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if aux.RecoveryBudget != nil {
		p.RecoveryBudget = &ServerRecoverBudget{
			MaxDuration: aux.RecoveryBudget.MaxDuration,
		}
		if aux.RecoveryBudget.RetryCount != nil {
			retry := *aux.RecoveryBudget.RetryCount
			p.RecoveryBudget.RetryCount = &retry
		}
	}

	return nil
}

// RecoverBudgetAlias for UnmarshalJSON helper.
type RecoverBudgetAlias struct {
	MaxDuration      int     `json:"max_duration"`
	RetryCount       *int    `json:"retry_count,omitempty"`
	LockScope        string  `json:"lock_scope,omitempty"`
	ForceRecovery    bool    `json:"force_recovery,omitempty"`
	AuthorizationRef *string `json:"authorization_ref,omitempty"`
}

// ServerRecoverResultJSON validates and unmarshals recover results from JSON.
func (r *ServerRecoverResult) UnmarshalJSON(data []byte) error {
	type Alias ServerRecoverResult
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(r),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	return nil
}

// RecoveryStepJSON is a JSON-friendly wrapper for recovery steps.
type RecoveryStepJSON struct {
	Step    string `json:"step"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// RecoveryWarningJSON is a JSON-friendly wrapper for warnings.
type RecoveryWarningJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
