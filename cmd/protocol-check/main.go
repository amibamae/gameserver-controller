package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
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

type ecmaRegexp regexp2.Regexp

func (re *ecmaRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(re).MatchString(value)
	return err == nil && matched
}

func (re *ecmaRegexp) String() string {
	return (*regexp2.Regexp)(re).String()
}

func compileECMA(expression string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(expression, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	return (*ecmaRegexp)(re), nil
}

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

func asObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func stringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func boolValue(object map[string]any, key string) bool {
	value, _ := object[key].(bool)
	return value
}

func arrayValue(object map[string]any, key string) []any {
	value, _ := object[key].([]any)
	return value
}

func contains(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func uniqueStrings(values []any) bool {
	seen := make(map[string]bool)
	for _, value := range values {
		str, ok := value.(string)
		if !ok || seen[str] {
			return false
		}
		seen[str] = true
	}
	return true
}

func parseTime(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("expected UTC Z timestamp, got %q", value)
	}
	return time.Parse(time.RFC3339Nano, value)
}
