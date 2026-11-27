package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// These characterization tests pin Register's checks, their order, and the
// copy it stores.

type adapterR5RegisterCase struct {
	name   string
	change func(*Descriptor)
	want   func(*adapterR5Fixture, Descriptor) string
}

func adapterR5RegisterCases() []adapterR5RegisterCase {
	return []adapterR5RegisterCase{
		{"descriptor that cannot be encoded", func(d *Descriptor) { d.Constraints = map[string]any{"bad": func() {}} },
			func(_ *adapterR5Fixture, d Descriptor) string {
				_, err := json.Marshal(d)
				return "encode adapter descriptor: " + err.Error()
			}},
		{"descriptor fails its schema", func(d *Descriptor) { d.AdapterID = "" },
			func(f *adapterR5Fixture, d Descriptor) string {
				encoded, _ := json.Marshal(d)
				return "invalid adapter descriptor: " + f.schemas.ValidateJSON("urn:gameserver-controller:"+f.reg.version+":common:capability-descriptor", encoded).Error()
			}},
		{"another protocol version", func(d *Descriptor) { d.ProtocolVersions = []string{"0.0.1"} },
			func(f *adapterR5Fixture, d Descriptor) string {
				return fmt.Sprintf("adapter %q does not support protocol %q", d.AdapterID, f.reg.version)
			}},
		{"capability outside the category", func(d *Descriptor) { d.Capabilities[1].Name = "storage.object.put" },
			adapterR5RegisterMessage(`capability "storage.object.put" does not belong to category "hosting"`)},
		{"duplicate capability", func(d *Descriptor) { d.Capabilities[1].Name = d.Capabilities[0].Name },
			adapterR5RegisterMessage(`adapter "hosting.r5" advertises duplicate capability "hosting.instance.create"`)},
		{"capabilities checked in order: duplicate first", func(d *Descriptor) {
			d.Capabilities[1].Name = d.Capabilities[0].Name
			d.Capabilities[2].Name = "game.save.request"
		}, adapterR5RegisterMessage(`adapter "hosting.r5" advertises duplicate capability "hosting.instance.create"`)},
		{"capabilities checked in order: category first", func(d *Descriptor) {
			d.Capabilities[1].Name = "game.save.request"
			d.Capabilities[2].Name = d.Capabilities[0].Name
		}, adapterR5RegisterMessage(`capability "game.save.request" does not belong to category "hosting"`)},
		{"category checked before schemas", func(d *Descriptor) {
			d.Capabilities[0].Name = "game.save.request"
			d.Capabilities[0].InputSchemaURI = "urn:gameserver-controller:missing"
		}, adapterR5RegisterMessage(`capability "game.save.request" does not belong to category "hosting"`)},
		{"unregistered input schema", func(d *Descriptor) { d.Capabilities[0].InputSchemaURI = "urn:gameserver-controller:missing" },
			adapterR5RegisterMessage(`capability "hosting.instance.create" references an unregistered input or output schema`)},
		{"unregistered output schema", func(d *Descriptor) { d.Capabilities[0].OutputSchemaURI = "urn:gameserver-controller:missing" },
			adapterR5RegisterMessage(`capability "hosting.instance.create" references an unregistered input or output schema`)},
		{"adapter already registered", func(d *Descriptor) { d.AdapterID = "hosting.example" },
			adapterR5RegisterMessage(`adapter "hosting.example" is already registered`)},
	}
}

func adapterR5RegisterMessage(message string) func(*adapterR5Fixture, Descriptor) string {
	return adapterR5Fixed[Descriptor](message)
}

func TestAdapterR5RegisterRejections(t *testing.T) {
	for _, test := range adapterR5RegisterCases() {
		t.Run(test.name, func(t *testing.T) {
			f := adapterR5NewFixture(t)
			implementation := adapterR5Example(t, "adapter-descriptor.hosting.json")
			implementation.descriptor.AdapterID = "hosting.r5"
			test.change(&implementation.descriptor)
			err := f.reg.Register(implementation)
			adapterR5Error(t, err, test.want(f, implementation.descriptor))
			if _, lookup := f.reg.Descriptor("hosting.r5"); !errors.Is(lookup, ErrAdapterNotFound) {
				t.Fatal("rejected adapter was registered")
			}
		})
	}
}

func TestAdapterR5RegisterRequiresRegistryAndImplementation(t *testing.T) {
	f := adapterR5NewFixture(t)
	var missing *Registry
	adapterR5Error(t, missing.Register(f.hosting), "registry and adapter implementation are required")
	adapterR5Error(t, f.reg.Register(nil), "registry and adapter implementation are required")
	if _, err := missing.Descriptor("hosting.example"); !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("nil registry Descriptor error = %v", err)
	}
	if missing.ProtocolVersion() != "" {
		t.Fatal("nil registry reported a protocol version")
	}
	if _, err := NewRegistry(nil); err == nil || err.Error() != "a loaded protocol schema registry is required" {
		t.Fatalf("NewRegistry(nil) error = %v", err)
	}
}

func TestAdapterR5RegisterStoresAnIndependentCopy(t *testing.T) {
	f := adapterR5NewFixture(t)
	implementation := adapterR5Example(t, "adapter-descriptor.paper.json")
	implementation.descriptor.AdapterID = "game.r5"
	if err := f.reg.Register(implementation); err != nil {
		t.Fatal(err)
	}
	implementation.descriptor.Capabilities[0].Name = "game.changed"
	implementation.descriptor.StreamTransportProfiles[0] = "changed"
	stored, err := f.reg.Descriptor("game.r5")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Capabilities[0].Name == "game.changed" || stored.StreamTransportProfiles[0] == "changed" {
		t.Fatal("registered descriptor shares memory with the implementation's")
	}
	if _, ok := f.reg.adapters["game.r5"].capabilities["game.backup.capture"]; !ok {
		t.Fatal("registered adapter is missing its advertised capability")
	}
}

func TestAdapterR5CapabilityPrefix(t *testing.T) {
	tests := map[Category]string{CategoryHosting: "hosting.", CategoryStorage: "storage.", CategoryExecution: "execution.",
		CategoryGame: "game.", CategoryIntegration: "integration.", "integrations": ""}
	for category, want := range tests {
		if got, ok := capabilityPrefix(category); got != want || ok != (want != "") {
			t.Fatalf("capabilityPrefix(%q) = %q, %v; want %q", category, got, ok, want)
		}
	}
}

// The descriptor schema's category enum rejects an unknown category before
// indexCapabilities runs, so this calls it directly.
func TestAdapterR5IndexCapabilitiesRejectsUnknownCategory(t *testing.T) {
	f := adapterR5NewFixture(t)
	descriptor := f.hosting.descriptor
	descriptor.Category = "integrations"
	capabilities, err := f.reg.indexCapabilities(descriptor)
	adapterR5Error(t, err, `adapter "hosting.example" has unsupported category "integrations"`)
	if capabilities != nil {
		t.Fatal("rejected descriptor returned capabilities")
	}
}
