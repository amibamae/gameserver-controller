package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Register validates an implementation's descriptor against the protocol
// and stores an independent copy with its capabilities indexed by name.
func (r *Registry) Register(implementation Implementation) error {
	if r == nil || implementation == nil {
		return errors.New("registry and adapter implementation are required")
	}
	descriptor := cloneDescriptor(implementation.Descriptor())
	if err := r.checkDescriptor(descriptor); err != nil {
		return err
	}
	capabilities, err := r.indexCapabilities(descriptor)
	if err != nil {
		return err
	}
	return r.add(registeredAdapter{implementation: implementation, descriptor: descriptor, capabilities: capabilities})
}

// checkDescriptor requires a schema-valid descriptor that supports the
// registry's protocol version.
func (r *Registry) checkDescriptor(descriptor Descriptor) error {
	descriptorSchema := "urn:gameserver-controller:" + r.version + ":common:capability-descriptor"
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return fmt.Errorf("encode adapter descriptor: %w", err)
	}
	if err := r.schemas.ValidateJSON(descriptorSchema, encoded); err != nil {
		return fmt.Errorf("invalid adapter descriptor: %w", err)
	}
	if !contains(descriptor.ProtocolVersions, r.version) {
		return fmt.Errorf("adapter %q does not support protocol %q", descriptor.AdapterID, r.version)
	}
	return nil
}

// indexCapabilities maps each capability by name, in descriptor order,
// requiring the category's name prefix, unique names, and registered input
// and output schemas.
func (r *Registry) indexCapabilities(descriptor Descriptor) (map[string]Capability, error) {
	prefix, ok := capabilityPrefix(descriptor.Category)
	if !ok {
		return nil, fmt.Errorf("adapter %q has unsupported category %q", descriptor.AdapterID, descriptor.Category)
	}
	capabilities := make(map[string]Capability, len(descriptor.Capabilities))
	for _, capability := range descriptor.Capabilities {
		if err := r.checkCapability(descriptor, prefix, capabilities, capability); err != nil {
			return nil, err
		}
		capabilities[capability.Name] = capability
	}
	return capabilities, nil
}

// checkCapability checks one capability against its category prefix, the
// capabilities indexed before it, and the loaded schemas.
func (r *Registry) checkCapability(descriptor Descriptor, prefix string, indexed map[string]Capability, capability Capability) error {
	if !strings.HasPrefix(capability.Name, prefix) {
		return fmt.Errorf("capability %q does not belong to category %q", capability.Name, descriptor.Category)
	}
	if _, duplicate := indexed[capability.Name]; duplicate {
		return fmt.Errorf("adapter %q advertises duplicate capability %q", descriptor.AdapterID, capability.Name)
	}
	if !r.schemas.HasSchema(capability.InputSchemaURI) || !r.schemas.HasSchema(capability.OutputSchemaURI) {
		return fmt.Errorf("capability %q references an unregistered input or output schema", capability.Name)
	}
	return nil
}

// add stores registered unless its adapter ID is already registered.
func (r *Registry) add(registered registeredAdapter) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := registered.descriptor.AdapterID
	if _, exists := r.adapters[id]; exists {
		return fmt.Errorf("adapter %q is already registered", id)
	}
	r.adapters[id] = registered
	return nil
}

func capabilityPrefix(category Category) (string, bool) {
	switch category {
	case CategoryHosting:
		return "hosting.", true
	case CategoryStorage:
		return "storage.", true
	case CategoryExecution:
		return "execution.", true
	case CategoryGame:
		return "game.", true
	case CategoryIntegration:
		return "integration.", true
	default:
		return "", false
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func cloneDescriptor(value Descriptor) Descriptor {
	value.ProtocolVersions = cloneSlice(value.ProtocolVersions)
	value.ContractVersions = cloneSlice(value.ContractVersions)
	value.RequiredPermissions = cloneSlice(value.RequiredPermissions)
	value.StreamTransportProfiles = cloneSlice(value.StreamTransportProfiles)
	value.Capabilities = cloneSlice(value.Capabilities)
	for i := range value.Capabilities {
		value.Capabilities[i].RequiredPermissions = cloneSlice(value.Capabilities[i].RequiredPermissions)
		value.Capabilities[i].Constraints = cloneMap(value.Capabilities[i].Constraints)
	}
	value.Constraints = cloneMap(value.Constraints)
	return value
}

func cloneSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	result := make([]T, len(values))
	copy(result, values)
	return result
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, entry := range value {
		result[key] = entry
	}
	return result
}
