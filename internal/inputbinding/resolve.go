// Package inputbinding resolves durable references between workflow steps.
package inputbinding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Binding struct {
	TargetPointer string `json:"target_pointer"`
	SourceStepID  string `json:"source_step_id"`
	SourcePointer string `json:"source_pointer"`
}

type Step struct {
	StepID  string
	Status  string
	Input   json.RawMessage
	Output  json.RawMessage
	Binding []Binding
}

// Resolve copies bound values from succeeded earlier steps into absent object
// members in the target step's input template. It never replaces a template
// value and rejects duplicate or overlapping destinations.
func Resolve(steps []Step, targetIndex int) (json.RawMessage, error) {
	if targetIndex < 0 || targetIndex >= len(steps) {
		return nil, errors.New("target step index is out of range")
	}
	if err := ValidatePlan(steps); err != nil {
		return nil, err
	}
	target := steps[targetIndex]
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(target.Input))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, errors.New("step input template must be a JSON object")
	}
	if len(target.Binding) == 0 {
		return append(json.RawMessage(nil), target.Input...), nil
	}
	seen := make(map[string]struct{}, len(target.Binding))
	targets := make([][]string, len(target.Binding))
	for i, binding := range target.Binding {
		path, err := pointerTokens(binding.TargetPointer)
		if err != nil || len(path) == 0 {
			return nil, fmt.Errorf("binding %d has an invalid target pointer", i)
		}
		if _, exists := seen[binding.TargetPointer]; exists {
			return nil, fmt.Errorf("binding destination %q is duplicated", binding.TargetPointer)
		}
		seen[binding.TargetPointer] = struct{}{}
		for _, prior := range targets[:i] {
			if prefixPath(prior, path) || prefixPath(path, prior) {
				return nil, errors.New("binding destinations overlap")
			}
		}
		targets[i] = path
	}
	for i, binding := range target.Binding {
		sourceIndex := -1
		for j := 0; j < targetIndex; j++ {
			if steps[j].StepID == binding.SourceStepID {
				sourceIndex = j
				break
			}
		}
		if sourceIndex < 0 || steps[sourceIndex].Status != "succeeded" {
			return nil, fmt.Errorf("binding source step %q is not an earlier succeeded step", binding.SourceStepID)
		}
		var source any
		decoder := json.NewDecoder(bytes.NewReader(steps[sourceIndex].Output))
		decoder.UseNumber()
		if err := decoder.Decode(&source); err != nil {
			return nil, fmt.Errorf("binding source step %q has no valid output", binding.SourceStepID)
		}
		sourcePath, err := pointerTokens(binding.SourcePointer)
		if err != nil {
			return nil, fmt.Errorf("binding %d has an invalid source pointer", i)
		}
		value, ok := get(source, sourcePath)
		if !ok {
			return nil, fmt.Errorf("binding source pointer %q does not exist", binding.SourcePointer)
		}
		if err := setAbsent(root, targets[i], value); err != nil {
			return nil, fmt.Errorf("binding target %q: %w", binding.TargetPointer, err)
		}
	}
	resolved, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode resolved step input: %w", err)
	}
	return resolved, nil
}

// ValidatePlan checks binding pointers, source ordering, and target conflicts
// without requiring the source steps to have executed yet.
func ValidatePlan(steps []Step) error {
	for i, step := range steps {
		if len(step.Binding) > 100 {
			return fmt.Errorf("step %q has too many input bindings", step.StepID)
		}
		targets := make([][]string, 0, len(step.Binding))
		seen := make(map[string]struct{}, len(step.Binding))
		for j, binding := range step.Binding {
			if len(binding.TargetPointer) > 1024 || len(binding.SourcePointer) > 1024 || binding.SourceStepID == "" || len(binding.SourceStepID) > 200 {
				return fmt.Errorf("step %q binding %d exceeds protocol limits", step.StepID, j)
			}
			path, err := pointerTokens(binding.TargetPointer)
			if err != nil || len(path) == 0 {
				return fmt.Errorf("step %q binding %d has an invalid target pointer", step.StepID, j)
			}
			if _, exists := seen[binding.TargetPointer]; exists {
				return fmt.Errorf("step %q has a duplicate binding destination", step.StepID)
			}
			seen[binding.TargetPointer] = struct{}{}
			for _, prior := range targets {
				if prefixPath(prior, path) || prefixPath(path, prior) {
					return fmt.Errorf("step %q has overlapping binding destinations", step.StepID)
				}
			}
			targets = append(targets, path)
			if _, err := pointerTokens(binding.SourcePointer); err != nil {
				return fmt.Errorf("step %q binding %d has an invalid source pointer", step.StepID, j)
			}
			sourceIsEarlier := false
			for priorIndex := 0; priorIndex < i; priorIndex++ {
				if steps[priorIndex].StepID == binding.SourceStepID {
					sourceIsEarlier = true
					break
				}
			}
			if !sourceIsEarlier {
				return fmt.Errorf("step %q binding source %q is not an earlier step", step.StepID, binding.SourceStepID)
			}
		}
	}
	return nil
}

func pointerTokens(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("JSON pointer must be empty or start with slash")
	}
	parts := strings.Split(pointer[1:], "/")
	for i, part := range parts {
		var decoded strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				decoded.WriteByte(part[j])
				continue
			}
			if j+1 >= len(part) || (part[j+1] != '0' && part[j+1] != '1') {
				return nil, errors.New("invalid JSON pointer escape")
			}
			j++
			if part[j] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[i] = decoded.String()
	}
	return parts, nil
}

func prefixPath(left, right []string) bool {
	if len(left) > len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func get(value any, path []string) (any, bool) {
	for _, token := range path {
		switch node := value.(type) {
		case map[string]any:
			var exists bool
			value, exists = node[token]
			if !exists {
				return nil, false
			}
		case []any:
			index, ok := arrayIndex(token, len(node))
			if !ok {
				return nil, false
			}
			value = node[index]
		default:
			return nil, false
		}
	}
	return value, true
}

func setAbsent(root map[string]any, path []string, value any) error {
	current := root
	for _, token := range path[:len(path)-1] {
		next, exists := current[token]
		if !exists {
			child := make(map[string]any)
			current[token] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return errors.New("target parent is not an object")
		}
		current = child
	}
	key := path[len(path)-1]
	if _, exists := current[key]; exists {
		return errors.New("target field already has a planned value")
	}
	current[key] = value
	return nil
}

func arrayIndex(token string, length int) (int, bool) {
	if token == "" || (len(token) > 1 && token[0] == '0') {
		return 0, false
	}
	index, err := strconv.Atoi(token)
	return index, err == nil && index >= 0 && index < length
}
