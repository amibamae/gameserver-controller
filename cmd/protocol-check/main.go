package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
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

// ecmaRegexp and compileECMA are defined in util.go

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

func run(root string) error {
	return runWithManifest(root, false)
}

func runWithManifest(root string, printManifest bool) error {
	pkg, err := loadPackage(root)
	if err != nil {
		return err
	}
	if err := pkg.validatePackage(); err != nil {
		return err
	}
	if err := pkg.runConformance(); err != nil {
		return err
	}
	if printManifest {
		manifest, err := pkg.packageManifest()
		if err != nil {
			return err
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

// asObject, stringValue, boolValue, arrayValue, contains, uniqueStrings, parseTime are defined in util.go
