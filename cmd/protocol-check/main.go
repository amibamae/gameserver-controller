package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	schemaIndexPath        = "protocol/schemas/index.json"
	provisionSchemaID      = "urn:gameserver-controller:0.21.0-draft:hosting:provision-request"
	operationEventSchemaID = "urn:gameserver-controller:0.21.0-draft:operations:operation-event"
)

var exampleSchemaIDs = map[string]string{
	"execution-unit-restart-input.json":             "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-input",
	"execution-unit-restart-output.json":            "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-output",
	"execution-unit-create-input.json":              "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-create-input",
	"execution-unit-create-output.json":             "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-create-output",
	"adapter-descriptor.hosting.json":               "urn:gameserver-controller:0.21.0-draft:common:capability-descriptor",
	"adapter-descriptor.paper.json":                 "urn:gameserver-controller:0.21.0-draft:common:capability-descriptor",
	"adapter-descriptor.storage.json":               "urn:gameserver-controller:0.21.0-draft:common:capability-descriptor",
	"adapter-descriptor.systemd.json":               "urn:gameserver-controller:0.21.0-draft:common:capability-descriptor",
	"adapter-descriptor.velocity.json":              "urn:gameserver-controller:0.21.0-draft:common:capability-descriptor",
	"adapter-initialize-stream-request.json":        "urn:gameserver-controller:0.21.0-draft:rpc:adapter-initialize-request",
	"adapter-initialize-stream-result.json":         "urn:gameserver-controller:0.21.0-draft:rpc:adapter-initialize-result",
	"adapter-invoke-request.json":                   "urn:gameserver-controller:0.21.0-draft:rpc:adapter-invoke-request",
	"backup-manifest.json":                          "urn:gameserver-controller:0.21.0-draft:storage:backup-manifest",
	"game-backup-prepare-input.json":                "urn:gameserver-controller:0.21.0-draft:game:backup-prepare-input",
	"game-backup-prepare-output.json":               "urn:gameserver-controller:0.21.0-draft:game:backup-prepare-output",
	"game-backup-capture-input.json":                "urn:gameserver-controller:0.21.0-draft:game:backup-capture-input",
	"game-backup-capture-invocation.json":           "urn:gameserver-controller:0.21.0-draft:rpc:adapter-invoke-request",
	"game-backup-capture-output.json":               "urn:gameserver-controller:0.21.0-draft:game:backup-capture-output",
	"game-backup-write-watermark.json":              "urn:gameserver-controller:0.21.0-draft:game:backup-write-watermark",
	"game-backup-validate-input.json":               "urn:gameserver-controller:0.21.0-draft:game:backup-validate-input",
	"game-backup-validate-output.json":              "urn:gameserver-controller:0.21.0-draft:game:backup-validate-output",
	"game-compatibility-check-input.json":           "urn:gameserver-controller:0.21.0-draft:game:compatibility-check-input",
	"game-compatibility-check-output.json":          "urn:gameserver-controller:0.21.0-draft:game:compatibility-check-output",
	"game-configure-input.json":                     "urn:gameserver-controller:0.21.0-draft:game:configure-input",
	"game-configure-output.json":                    "urn:gameserver-controller:0.21.0-draft:game:configure-output",
	"game-data-describe-input.json":                 "urn:gameserver-controller:0.21.0-draft:game:data-describe-input",
	"game-data-describe-output.json":                "urn:gameserver-controller:0.21.0-draft:game:data-describe-output",
	"game-health-check-input.json":                  "urn:gameserver-controller:0.21.0-draft:game:health-check-input",
	"game-health-check-output.json":                 "urn:gameserver-controller:0.21.0-draft:game:health-check-output",
	"game-migrate-input.json":                       "urn:gameserver-controller:0.21.0-draft:game:migrate-input",
	"game-migrate-output.json":                      "urn:gameserver-controller:0.21.0-draft:game:migrate-output",
	"game-install-input.json":                       "urn:gameserver-controller:0.21.0-draft:game:install-input",
	"game-install-output.json":                      "urn:gameserver-controller:0.21.0-draft:game:install-output",
	"game-readiness-check-input.json":               "urn:gameserver-controller:0.21.0-draft:game:readiness-check-input",
	"game-readiness-check-output.json":              "urn:gameserver-controller:0.21.0-draft:game:readiness-check-output",
	"game-release-describe-input.json":              "urn:gameserver-controller:0.21.0-draft:game:release-describe-input",
	"game-release-describe-output.json":             "urn:gameserver-controller:0.21.0-draft:game:release-describe-output",
	"game-save-request-input.json":                  "urn:gameserver-controller:0.21.0-draft:game:save-request-input",
	"game-save-request-output.json":                 "urn:gameserver-controller:0.21.0-draft:game:save-request-output",
	"game-validate-installation-input.json":         "urn:gameserver-controller:0.21.0-draft:game:validate-installation-input",
	"game-validate-installation-output.json":        "urn:gameserver-controller:0.21.0-draft:game:validate-installation-output",
	"operation-accepted.json":                       "urn:gameserver-controller:0.21.0-draft:operations:operation",
	"hosting-instance-inspect-input.json":           "urn:gameserver-controller:0.21.0-draft:hosting:instance-inspect-input",
	"hosting-instance-inspect-output.json":          "urn:gameserver-controller:0.21.0-draft:hosting:instance-inspect-output",
	"hosting-bootstrap-deliver-input.json":          "urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-deliver-input",
	"hosting-bootstrap-inspect-input.json":          "urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-inspect-input",
	"hosting-bootstrap-execution-output.json":       "urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-execution-output",
	"hosting-instance-start-input.json":             "urn:gameserver-controller:0.21.0-draft:hosting:instance-start-input",
	"hosting-instance-start-output.json":            "urn:gameserver-controller:0.21.0-draft:hosting:instance-start-output",
	"hosting-instance-stop-input.json":              "urn:gameserver-controller:0.21.0-draft:hosting:instance-stop-input",
	"hosting-instance-stop-output.json":             "urn:gameserver-controller:0.21.0-draft:hosting:instance-stop-output",
	"hosting-instance-wait-input.json":              "urn:gameserver-controller:0.21.0-draft:hosting:instance-wait-input",
	"hosting-instance-wait-no-ref-input.json":       "urn:gameserver-controller:0.21.0-draft:hosting:instance-wait-input",
	"hosting-instance-wait-output.json":             "urn:gameserver-controller:0.21.0-draft:hosting:instance-wait-output",
	"hosting-instance-reboot-input.json":            "urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-input",
	"hosting-instance-reboot-output.json":           "urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-output",
	"hosting-instance-destroy-input.json":           "urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input",
	"hosting-instance-destroy-output.json":          "urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-output",
	"hosting-os-offerings-list-input.json":          "urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-input",
	"hosting-os-offerings-list-output.json":         "urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-output",
	"hosting-plans-list-input.json":                 "urn:gameserver-controller:0.21.0-draft:hosting:plans-list-input",
	"hosting-plans-list-output.json":                "urn:gameserver-controller:0.21.0-draft:hosting:plans-list-output",
	"hosting-regions-list-input.json":               "urn:gameserver-controller:0.21.0-draft:hosting:regions-list-input",
	"hosting-regions-list-output.json":              "urn:gameserver-controller:0.21.0-draft:hosting:regions-list-output",
	"operation-event.json":                          "urn:gameserver-controller:0.21.0-draft:operations:operation-event",
	"operation-result.backup.succeeded.json":        "urn:gameserver-controller:0.21.0-draft:operations:server-backup-result",
	"operation-result.destroy.succeeded.json":       "urn:gameserver-controller:0.21.0-draft:operations:server-destroy-result",
	"operation-result.restore.succeeded.json":       "urn:gameserver-controller:0.21.0-draft:operations:server-restore-result",
	"operation-result.succeeded.json":               "urn:gameserver-controller:0.21.0-draft:operations:server-provision-result",
	"operation-succeeded.json":                      "urn:gameserver-controller:0.21.0-draft:operations:operation",
	"provision-request.json":                        "urn:gameserver-controller:0.21.0-draft:hosting:provision-request",
	"proxy-backend-drain-input.json":                "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-input",
	"proxy-backend-drain-output.json":               "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-output",
	"proxy-backend-inspect-input.json":              "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-inspect-input",
	"proxy-backend-inspect-output.json":             "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-inspect-output",
	"proxy-backend-players-inspect-input.json":      "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-players-inspect-input",
	"proxy-backend-players-inspect-output.json":     "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-players-inspect-output",
	"proxy-backend-register-input.json":             "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-input",
	"proxy-backend-register-output.json":            "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-output",
	"proxy-backend-unregister-input.json":           "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-input",
	"proxy-backend-unregister-output.json":          "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-output",
	"server-define-result.succeeded.json":           "urn:gameserver-controller:0.21.0-draft:operations:server-define-result",
	"server-start-result.succeeded.json":            "urn:gameserver-controller:0.21.0-draft:operations:server-start-result",
	"server-stop-result.succeeded.json":             "urn:gameserver-controller:0.21.0-draft:operations:server-stop-result",
	"storage-checksum.json":                         "urn:gameserver-controller:0.21.0-draft:storage:storage-checksum",
	"storage-container-reference.json":              "urn:gameserver-controller:0.21.0-draft:storage:storage-container-reference",
	"storage-object-copy-input.json":                "urn:gameserver-controller:0.21.0-draft:storage:object-copy-input",
	"storage-object-copy-output.json":               "urn:gameserver-controller:0.21.0-draft:storage:object-copy-output",
	"storage-object-delete-input.json":              "urn:gameserver-controller:0.21.0-draft:storage:object-delete-input",
	"storage-object-delete-output.json":             "urn:gameserver-controller:0.21.0-draft:storage:object-delete-output",
	"storage-object-get-input.json":                 "urn:gameserver-controller:0.21.0-draft:storage:object-get-input",
	"storage-object-get-output.json":                "urn:gameserver-controller:0.21.0-draft:storage:object-get-output",
	"storage-object-head-input.json":                "urn:gameserver-controller:0.21.0-draft:storage:object-head-input",
	"storage-object-head-output.json":               "urn:gameserver-controller:0.21.0-draft:storage:object-head-output",
	"storage-object-list-input.json":                "urn:gameserver-controller:0.21.0-draft:storage:object-list-input",
	"storage-object-list-output.json":               "urn:gameserver-controller:0.21.0-draft:storage:object-list-output",
	"storage-object-put-input.json":                 "urn:gameserver-controller:0.21.0-draft:storage:object-put-input",
	"storage-object-put-output.json":                "urn:gameserver-controller:0.21.0-draft:storage:object-put-output",
	"storage-object-reference.json":                 "urn:gameserver-controller:0.21.0-draft:storage:storage-object-reference",
	"storage-object-retention-inspect-input.json":   "urn:gameserver-controller:0.21.0-draft:storage:object-retention-inspect-input",
	"storage-object-retention-inspect-output.json":  "urn:gameserver-controller:0.21.0-draft:storage:object-retention-inspect-output",
	"storage-object-target.json":                    "urn:gameserver-controller:0.21.0-draft:storage:storage-object-target",
	"storage-object-verify-checksum-input.json":     "urn:gameserver-controller:0.21.0-draft:storage:object-verify-checksum-input",
	"storage-object-verify-checksum-output.json":    "urn:gameserver-controller:0.21.0-draft:storage:object-verify-checksum-output",
	"storage-object-versioning-inspect-input.json":  "urn:gameserver-controller:0.21.0-draft:storage:object-versioning-inspect-input",
	"storage-object-versioning-inspect-output.json": "urn:gameserver-controller:0.21.0-draft:storage:object-versioning-inspect-output",
	"storage-transfer-reference.json":               "urn:gameserver-controller:0.21.0-draft:storage:storage-transfer-reference",
	"systemd-unit-delete-input.json":                "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-delete-input",
	"systemd-unit-delete-output.json":               "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-delete-output",
	"systemd-unit-health-inspect-input.json":        "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-input",
	"systemd-unit-health-inspect-output.json":       "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-output",
	"systemd-unit-force-stop-input.json":            "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-force-stop-input",
	"systemd-unit-force-stop-output.json":           "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-force-stop-output",
	"systemd-unit-graceful-stop-input.json":         "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-graceful-stop-input",
	"systemd-unit-graceful-stop-output.json":        "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-graceful-stop-output",
	"systemd-unit-start-input.json":                 "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-start-input",
	"systemd-unit-start-output.json":                "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-start-output",
	"systemd-unit-logs-read-input.json":             "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-input",
	"systemd-unit-logs-read-output.json":            "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-output",
	"velocity-player-transfer-request.json":         "urn:gameserver-controller:0.21.0-draft:integrations:velocity-player-transfer-request",
	"velocity-player-transfer-result.json":          "urn:gameserver-controller:0.21.0-draft:integrations:velocity-player-transfer-result",
}

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
		len(pkg.entries), len(exampleSchemaIDs), pkg.fixtureCount(), pkg.version)
	fmt.Printf("Conformance passed: %d fixtures.\n", pkg.fixtureCount())
	return nil
}

func loadPackage(root string) (*protocolPackage, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	indexBytes, err := os.ReadFile(filepath.Join(absoluteRoot, schemaIndexPath))
	if err != nil {
		return nil, fmt.Errorf("read schema index: %w", err)
	}
	indexValue, err := parseJSON(indexBytes, schemaIndexPath)
	if err != nil {
		return nil, err
	}
	index := asObject(indexValue)
	if index == nil {
		return nil, fmt.Errorf("%s must be a JSON object", schemaIndexPath)
	}
	version, ok := index["protocol_version"].(string)
	if !ok || version == "" {
		return nil, fmt.Errorf("schema index must declare protocol_version")
	}
	if index["json_schema_dialect"] != "https://json-schema.org/draft/2020-12/schema" {
		return nil, fmt.Errorf("schema index must select JSON Schema Draft 2020-12")
	}
	rawEntries, ok := index["schemas"].([]any)
	if !ok || len(rawEntries) == 0 {
		return nil, fmt.Errorf("schema index must contain a non-empty schemas array")
	}

	pkg := &protocolPackage{
		root:       absoluteRoot,
		version:    version,
		schemaDocs: make(map[string]any),
		rawSchemas: make(map[string][]byte),
		validators: make(map[string]*jsonschema.Schema),
	}
	indexedPaths := make(map[string]bool)
	for _, rawEntry := range rawEntries {
		entryMap := asObject(rawEntry)
		if entryMap == nil {
			return nil, fmt.Errorf("schema index entries must be objects")
		}
		relative, okPath := entryMap["path"].(string)
		id, okID := entryMap["id"].(string)
		if !okPath || !okID || relative == "" || id == "" {
			return nil, fmt.Errorf("schema index entry is missing path or id: %#v", rawEntry)
		}
		clean := filepath.Clean(filepath.FromSlash(relative))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("unsafe schema path in index: %s", relative)
		}
		if indexedPaths[relative] {
			return nil, fmt.Errorf("duplicate schema path in index: %s", relative)
		}
		indexedPaths[relative] = true
		path := filepath.Join(absoluteRoot, "protocol", "schemas", clean)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read indexed schema %s: %w", relative, err)
		}
		doc, err := parseJSON(data, filepath.Join("protocol", "schemas", relative))
		if err != nil {
			return nil, err
		}
		docMap := asObject(doc)
		if docMap == nil {
			return nil, fmt.Errorf("%s must be a JSON object", relative)
		}
		if docMap["$id"] != id {
			return nil, fmt.Errorf("%s: index id does not match schema $id (%q != %v)", relative, id, docMap["$id"])
		}
		if docMap["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			return nil, fmt.Errorf("%s: schema must declare Draft 2020-12", relative)
		}
		if _, exists := pkg.schemaDocs[id]; exists {
			return nil, fmt.Errorf("duplicate schema $id: %s", id)
		}
		pkg.entries = append(pkg.entries, schemaEntry{Path: relative, ID: id})
		pkg.schemaDocs[id] = docMap
		pkg.rawSchemas[id] = data
	}
	if err := pkg.checkSchemaInventory(indexedPaths); err != nil {
		return nil, err
	}
	if err := pkg.compileSchemas(); err != nil {
		return nil, err
	}
	return pkg, nil
}

func (pkg *protocolPackage) checkSchemaInventory(indexed map[string]bool) error {
	root := filepath.Join(pkg.root, "protocol", "schemas")
	var actual []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		actual = append(actual, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return fmt.Errorf("discover schema files: %w", err)
	}
	sort.Strings(actual)
	for _, path := range actual {
		if !indexed[path] {
			return fmt.Errorf("schema file is not listed in index.json: %s", path)
		}
	}
	for path := range indexed {
		found := false
		for _, actualPath := range actual {
			if path == actualPath {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("index entry is not a discoverable schema file: %s", path)
		}
	}
	return nil
}

func (pkg *protocolPackage) compileSchemas() error {
	ids := make([]string, 0, len(pkg.rawSchemas))
	for id := range pkg.rawSchemas {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseRegexpEngine(compileECMA)
	for _, id := range ids {
		if err := pkg.checkSchemaReferences(id); err != nil {
			return err
		}
	}
	for _, id := range ids {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(pkg.rawSchemas[id]))
		if err != nil {
			return fmt.Errorf("parse schema %s: %w", id, err)
		}
		if err := compiler.AddResource(id, doc); err != nil {
			return fmt.Errorf("register schema %s: %w", id, err)
		}
	}
	for _, id := range ids {
		compiled, err := compiler.Compile(id)
		if err != nil {
			return fmt.Errorf("compile schema %s: %w", id, err)
		}
		pkg.validators[id] = compiled
	}
	return nil
}

func (pkg *protocolPackage) checkSchemaReferences(id string) error {
	doc := pkg.schemaDocs[id]
	var refs []string
	collectRefs(doc, &refs)
	for _, ref := range refs {
		base := strings.SplitN(ref, "#", 2)[0]
		if strings.HasPrefix(base, "urn:") {
			if _, ok := pkg.schemaDocs[base]; !ok {
				return fmt.Errorf("%s: unresolved local schema reference: %s", id, ref)
			}
		} else if base != "" {
			return fmt.Errorf("%s: external schema reference is not allowed in the closed registry: %s", id, ref)
		}
	}
	return nil
}

func (pkg *protocolPackage) validatePackage() error {
	if err := pkg.validateExamples(); err != nil {
		return err
	}
	if err := pkg.validateFixtures(); err != nil {
		return err
	}
	return pkg.validateOpenAPI()
}

func (pkg *protocolPackage) validateExamples() error {
	examplesDir := filepath.Join(pkg.root, "protocol", "examples")
	paths, err := filepath.Glob(filepath.Join(examplesDir, "*.json"))
	if err != nil {
		return err
	}
	actual := make(map[string]bool)
	for _, path := range paths {
		actual[filepath.Base(path)] = true
	}
	expected := make(map[string]bool)
	for name := range exampleSchemaIDs {
		expected[name] = true
	}
	for name := range actual {
		if !expected[name] {
			return fmt.Errorf("example is not mapped to a schema in validator: %s", name)
		}
	}
	for name, id := range exampleSchemaIDs {
		path := filepath.Join(examplesDir, name)
		if !actual[name] {
			return fmt.Errorf("mapped example does not exist: %s", name)
		}
		doc, err := parseJSONFile(path)
		if err != nil {
			return err
		}
		if object := asObject(doc); object != nil {
			if capabilities, ok := object["capabilities"].([]any); ok {
				for i, rawCapability := range capabilities {
					capability := asObject(rawCapability)
					if capability == nil {
						return fmt.Errorf("%s: capability %d must be an object", name, i)
					}
					for _, field := range []string{"input_schema_uri", "output_schema_uri"} {
						uri, ok := capability[field].(string)
						if !ok {
							return fmt.Errorf("%s: capability %d %s must be a string", name, i, field)
						}
						if _, ok := pkg.validators[uri]; !ok {
							return fmt.Errorf("%s: capability %d %s does not resolve in the local schema registry: %q", name, i, field, uri)
						}
					}
				}
			}
		}
		if err := pkg.validateSchema(id, doc); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (pkg *protocolPackage) validateFixtures() error {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return fmt.Errorf("no conformance fixtures found")
	}
	seen := make(map[string]bool)
	for _, path := range paths {
		fixture, err := parseJSONFile(path)
		if err != nil {
			return fmt.Errorf("%s: invalid conformance fixture JSON: %w", filepath.Base(path), err)
		}
		doc := asObject(fixture)
		name := filepath.Base(path)
		id, ok := doc["fixture_id"].(string)
		if !ok || id == "" {
			return fmt.Errorf("%s: fixture_id is required", name)
		}
		if seen[id] {
			return fmt.Errorf("%s: duplicate fixture_id: %s", name, id)
		}
		seen[id] = true
		if doc["protocol_version"] != pkg.version {
			return fmt.Errorf("%s: fixture protocol_version must match schema index", name)
		}
		if asObject(doc["expected"]) == nil {
			return fmt.Errorf("%s: expected must be an object", name)
		}
	}
	return nil
}

func (pkg *protocolPackage) validateOpenAPI() error {
	path := filepath.Join(pkg.root, "protocol", "management-api", "openapi.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read OpenAPI document: %w", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse OpenAPI document: %w", err)
	}
	if document["openapi"] != "3.2.1" {
		return fmt.Errorf("OpenAPI document must declare 3.2.1, found %v", document["openapi"])
	}
	if err := validateOpenAPIStructure(document); err != nil {
		return fmt.Errorf("OpenAPI document does not match the OAS 3.2 schema: %w", err)
	}
	components := asObject(document["components"])
	apiSchemas := asObject(components["schemas"])
	expectedRefs := map[string]string{
		"Operation": "../schemas/operations/operation.schema.json",
		"Server":    "../schemas/lifecycle/server.schema.json",
	}
	for name, expected := range expectedRefs {
		component := asObject(apiSchemas[name])
		if component == nil || component["$ref"] != expected {
			return fmt.Errorf("OpenAPI component %s must reference the canonical schema %s", name, expected)
		}
	}
	var refs []string
	collectRefs(document, &refs)
	for _, ref := range refs {
		if strings.HasPrefix(ref, "#") {
			continue
		}
		relative := strings.SplitN(ref, "#", 2)[0]
		if relative == "" {
			continue
		}
		target := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(relative)))
		relToRoot, err := filepath.Rel(pkg.root, target)
		if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			return fmt.Errorf("OpenAPI reference escapes the repository: %s", ref)
		}
		if _, err := os.Stat(target); err != nil {
			return fmt.Errorf("OpenAPI reference target does not exist: %s", ref)
		}
		schemaRoot := filepath.Join(pkg.root, "protocol", "schemas")
		relToSchemas, err := filepath.Rel(schemaRoot, target)
		if err != nil || relToSchemas == ".." || strings.HasPrefix(relToSchemas, ".."+string(filepath.Separator)) {
			return fmt.Errorf("OpenAPI external reference must point to the local schema package: %s", ref)
		}
		if !pkg.pathIsIndexed(filepath.ToSlash(relToSchemas)) {
			return fmt.Errorf("OpenAPI reference target is not registered in schemas/index.json: %s", ref)
		}
	}
	return nil
}

func (pkg *protocolPackage) pathIsIndexed(path string) bool {
	for _, entry := range pkg.entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func (pkg *protocolPackage) validateSchema(id string, value any) error {
	validator, ok := pkg.validators[id]
	if !ok {
		return fmt.Errorf("mapped schema is missing from index.json: %s", id)
	}
	return validator.Validate(value)
}

func (pkg *protocolPackage) fixtureCount() int {
	paths, _ := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	return len(paths)
}

func parseJSONFile(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseJSON(data, path)
}

func parseJSON(data []byte, source string) (any, error) {
	if err := validateJSONUnicode(data); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON Unicode: %w", source, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", source, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s: trailing data after JSON value", source)
		}
		return nil, fmt.Errorf("%s: invalid trailing JSON: %w", source, err)
	}
	if err := checkSafeIntegers(value, source, "$"); err != nil {
		return nil, err
	}
	return value, nil
}

func parseJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch delim, ok := token.(json.Delim); {
	case ok && delim == '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object member name is not a string")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate JSON object member: %s", key)
			}
			value, err := parseJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("unterminated JSON object")
		}
		return object, nil
	case ok && delim == '[':
		var values []any
		for decoder.More() {
			value, err := parseJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("unterminated JSON array")
		}
		return values, nil
	case ok:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	default:
		if number, ok := token.(json.Number); ok {
			if _, err := number.Float64(); err != nil {
				return nil, fmt.Errorf("invalid JSON number %q", number)
			}
		}
		return token, nil
	}
}

func checkSafeIntegers(value any, source, location string) error {
	switch typed := value.(type) {
	case json.Number:
		raw := typed.String()
		rational, ok := new(big.Rat).SetString(raw)
		if !ok {
			return fmt.Errorf("%s%s: invalid JSON number %q", source, location, raw)
		}
		if rational.Denom().Cmp(big.NewInt(1)) != 0 {
			return nil
		}
		limit, _ := new(big.Int).SetString("9007199254740991", 10)
		if new(big.Int).Abs(rational.Num()).Cmp(limit) > 0 {
			return fmt.Errorf("%s%s: integer exceeds the interoperable JSON safe-integer range", source, location)
		}
	case map[string]any:
		for key, child := range typed {
			if err := checkSafeIntegers(child, source, location+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := checkSafeIntegers(child, source, fmt.Sprintf("%s[%d]", location, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectRefs(value any, refs *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			*refs = append(*refs, ref)
		}
		for _, child := range typed {
			collectRefs(child, refs)
		}
	case []any:
		for _, child := range typed {
			collectRefs(child, refs)
		}
	}
}

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
