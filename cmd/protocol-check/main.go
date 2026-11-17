package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	schemaIndexPath        = "protocol/schemas/index.json"
	provisionSchemaID      = "urn:gameserver-controller:0.21.0-draft:hosting:provision-request"
	operationEventSchemaID = "urn:gameserver-controller:0.21.0-draft:operations:operation-event"
)

type schemaEntry struct {
	Path string
	ID   string
}

// protocolPackage holds the validated protocol package with loaded schemas, examples, and validators.
type protocolPackage struct {
	root       string
	version    string
	entries    []schemaEntry
	schemaDocs map[string]any
	rawSchemas map[string][]byte
	validators map[string]*jsonschema.Schema
	// exampleCount is the number of examples validateExamples checked.
	exampleCount int
}

func main() {
	manifest := flag.Bool("manifest", false, "print the validated protocol package manifest as JSON")
	flag.Parse()
	if err := runWithManifest(".", *manifest); err != nil {
		fmt.Fprintln(os.Stderr, "Protocol validation failed:")
		fmt.Fprintln(os.Stderr, " -", err)
		os.Exit(1)
	}
}

// run is a convenience function that runs protocol validation without printing manifest.
func run(root string) error {
	return runWithManifest(root, false)
}

// runWithManifest performs full protocol package validation and optionally prints the manifest.
func runWithManifest(root string, printManifest bool) error {
	pkg, err := loadPackage(root)
	if err != nil {
		return fmt.Errorf("loadPackage(%q): %w", root, err)
	}
	if err := pkg.validatePackage(); err != nil {
		return fmt.Errorf("validatePackage(): %w", err)
	}
	if err := pkg.runConformance(); err != nil {
		return fmt.Errorf("runConformance(): %w", err)
	}
	if printManifest {
		manifest, err := pkg.packageManifest()
		if err != nil {
			return fmt.Errorf("packageManifest(): %w", err)
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(manifest)
	}
	fmt.Printf("Protocol validation passed: %d schemas, %d examples, %d conformance fixtures, protocol %s.\n",
		len(pkg.entries), pkg.exampleCount, pkg.fixtureCount(), pkg.version)
	fmt.Printf("Conformance passed: %d fixtures.\n", pkg.fixtureCount())
	return nil
}

// exampleIndexFile is the language-neutral example-to-schema map. It is the
// only mapping: this validator and other implementations both read it.
const exampleIndexFile = "index.json"
