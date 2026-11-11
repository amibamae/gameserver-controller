package adapter_test

import (
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

func TestRegisterAcceptsVelocityIntegrationDescriptor(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor adapter.Descriptor
	readExample(t, "adapter-descriptor.velocity.json", &descriptor)
	if err := registry.Register(&captureAdapter{descriptor: descriptor}); err != nil {
		t.Fatalf("protocol Velocity descriptor was rejected: %v", err)
	}
	registered, err := registry.Descriptor(descriptor.AdapterID)
	if err != nil {
		t.Fatal(err)
	}
	if len(registered.Capabilities) != len(descriptor.Capabilities) {
		t.Fatalf("registered %d capabilities, want %d", len(registered.Capabilities), len(descriptor.Capabilities))
	}
	found := false
	for _, capability := range registered.Capabilities {
		if capability.Name == "integration.proxy.backend.register" {
			found = true
		}
	}
	if !found {
		t.Fatal("integration.proxy.backend.register was not registered")
	}
}

func TestRegisterRejectsPluralIntegrationsPrefix(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor adapter.Descriptor
	readExample(t, "adapter-descriptor.velocity.json", &descriptor)
	descriptor.Capabilities[0].Name = "integrations." + strings.TrimPrefix(descriptor.Capabilities[0].Name, "integration.")
	err = registry.Register(&captureAdapter{descriptor: descriptor})
	if err == nil || !strings.Contains(err.Error(), "does not belong to category") {
		t.Fatalf("plural integrations prefix: got %v, want category prefix rejection", err)
	}
}
