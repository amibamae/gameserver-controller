package simulation

import (
	"encoding/json"
	"fmt"
	"time"
)

// Environment configures the simulation environment.
type Environment struct {
	name           string
	timeout        time.Duration
	failRate       float64
	adapters       map[string]Adapter
	scenarioHistory []ScenarioResult
}

// Adapter interface for simulation adapters.
type Adapter interface {
	Capabilities() []string
}

// Scenario defines a test scenario to execute.
type Scenario struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description,omitempty"`
	Steps       []ScenarioStep            `json:"steps"`
	Expectations map[string]ScenarioExpectation `json:"expectations,omitempty"`
}

// ScenarioStep defines a single step in a scenario.
type ScenarioStep struct {
	Name      string    `json:"name"`
	Call      string    `json:"call"`       // e.g., "vultr.hosting.create"
	Parameters json.RawMessage `json:"params"`
}

// ScenarioExpectation defines an expected result for a step.
type ScenarioExpectation struct {
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Validated  bool   `json:"validated,omitempty"`
}

// NewEnvironment creates a new simulation environment.
func NewEnvironment(config EnvironmentConfig) *Environment {
	return &Environment{
		name:      config.Name,
		timeout:   config.Timeout,
		failRate:  config.FailRate,
		adapters:  make(map[string]Adapter),
	}
}

// EnvironmentConfig provides configuration for the simulation environment.
type EnvironmentConfig struct {
	Name      string
	Timeout    time.Duration
	FailRate   float64
}

// SetAdapter registers a fake adapter in the simulation environment.
func (env *Environment) SetAdapter(adapterType string, adapter Adapter) {
	env.adapters[adapterType] = adapter
}

// ExecuteScenario runs a test scenario against the simulation environment.
func (env *Environment) ExecuteScenario(scenario Scenario) (*ScenarioResult, error) {
	result := &ScenarioResult{
		Name:        scenario.Name,
		Description: scenario.Description,
		Steps:       make([]StepResult, 0),
		Passed:      true,
	}

	startTime := time.Now()

	for _, step := range scenario.Steps {
		stepStartTime := time.Now()

		result.Steps = append(result.Steps, StepResult{
			Name: step.Name,
			Start: stepStartTime.UnixMilli(),
		})

		if len(scenario.Expectations) > 0 {
			expectation := scenario.Expectations[step.Call]
			if expectation.Status != "" && expectation.Status != "any" {
				result.Steps[len(result.Steps)-1].Status = expectation.Status
			} else if len(env.adapters) > 0 {
				// Try to find appropriate adapter and execute call
				adapter := env.getAdapter(step.Call)
				if adapter == nil {
					result.Steps[len(result.Steps)-1].Error = fmt.Sprintf("no adapter found for: %s", step.Call)
					result.Passed = false
					continue
				}

				caps := adapter.Capabilities()
				found := false
				for _, cap := range caps {
					if isMatchingCall(step.Call, cap) {
						found = true
						break
					}
				}

				if !found {
					result.Steps[len(result.Steps)-1].Error = fmt.Sprintf("capability not found in adapter: %s", step.Call)
					result.Passed = false
					continue
				}

				response, err := adapter.ExecuteCall(step.Call, step.Parameters)
				if err != nil {
					result.Steps[len(result.Steps)-1].Error = err.Error()
					result.Passed = false
					continue
				}

				result.Steps[len(result.Steps)-1].Response = string(response)
			}
		} else {
			result.Steps[len(result.Steps)-1].Status = "success"
		}

		result.Steps[len(result.Steps)-1].End = time.Now().UnixMilli()
	}

	result.Duration = time.Since(startTime)
	result.Start = startTime.UnixMilli()
	result.End = time.Now().UnixMilli()

	env.scenarioHistory = append(env.scenarioHistory, result)

	return result, nil
}

// getAdapter retrieves an adapter for a given call capability.
func (env *Environment) getAdapter(call string) Adapter {
	for _, adapter := range env.adapters {
		caps := adapter.Capabilities()
		for _, cap := range caps {
			if isMatchingCall(call, cap) {
				return adapter
			}
		}
	}
	return nil
}

// isMatchingCall checks if a capability string matches a call prefix.
func isMatchingCall(call, cap string) bool {
	prefixes := []string{
		call + ".",
		call + "/",
	}
	
	for _, prefix := range prefixes {
		if len(call)+1 < len(cap) && cap[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// executeCall executes a call on an adapter (simplified interface).
func (adapter Adapter) ExecuteCall(call string, params json.RawMessage) (json.RawMessage, error) {
	caps := adapter.Capabilities()
	for _, cap := range caps {
		if isMatchingCall(call, cap) {
			return adapter.Execute(cap, params)
		}
	}
	return nil, fmt.Errorf("no matching capability for: %s", call)
}

// Execute performs the actual API call implementation on an adapter.
func (adapter Adapter) Execute(call string, params json.RawMessage) (json.RawMessage, error) {
	// Default stub implementation - adapters should override this
	return nil, fmt.Errorf("execute not implemented for: %s", call)
}

// ScenarioResult represents the result of a scenario execution.
type ScenarioResult struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Steps       []StepResult           `json:"steps"`
	Passed      bool                   `json:"passed"`
	Duration    time.Duration          `json:"duration,omitempty"`
	Start       int64                  `json:"start,omitempty"`
	End         int64                  `json:"end,omitempty"`
}

// StepResult represents the result of a scenario step.
type StepResult struct {
	Name      string                 `json:"name"`
	Status    string                 `json:"status,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Response  json.RawMessage        `json:"response,omitempty"`
	Start     int64                  `json:"start,omitempty"`
	End       int64                  `json:"end,omitempty"`
}
