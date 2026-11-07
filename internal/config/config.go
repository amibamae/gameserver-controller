// Package config parses and validates the initial side-effect-free local
// controller configuration format.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1

var (
	identifierPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	adapterIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9.-]{1,159}$`)
	secretNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	credentialNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

const systemdCredentialScheme = "systemd-credential://"

type Config struct {
	SchemaVersion int              `yaml:"schema_version"`
	Controller    Controller       `yaml:"controller"`
	Adapters      []AdapterProfile `yaml:"adapters"`
	Environments  []Environment    `yaml:"environments"`
	Servers       []ServerProfile  `yaml:"servers"`
}

type Controller struct {
	Mode       string     `yaml:"mode"`
	StateStore StateStore `yaml:"state_store"`
	Defaults   Defaults   `yaml:"defaults"`
	Audit      Audit      `yaml:"audit"`
}

type StateStore struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

type Defaults struct {
	OperationTimeout string `yaml:"operation_timeout"`
	RetryLimit       *int   `yaml:"retry_limit"`
}

type Audit struct {
	Enabled     bool   `yaml:"enabled"`
	Destination string `yaml:"destination"`
}

type AdapterProfile struct {
	ID         string                     `yaml:"id"`
	Category   string                     `yaml:"category"`
	SecretRefs map[string]SecretReference `yaml:"secret_refs"`
}

type SecretReference struct {
	SecretRef       string   `yaml:"secret_ref"`
	Purpose         string   `yaml:"purpose"`
	PermissionScope []string `yaml:"permission_scope"`
	VersionRef      string   `yaml:"version_ref"`
}

type Environment struct {
	ID                     string `yaml:"id"`
	Classification         string `yaml:"classification"`
	ProductionBackupAccess string `yaml:"production_backup_access"`
}

type ServerProfile struct {
	ID            string          `yaml:"id"`
	EnvironmentID string          `yaml:"environment_id"`
	Adapters      ServerAdapters  `yaml:"adapters"`
	Lifecycle     ServerLifecycle `yaml:"lifecycle"`
}

type ServerAdapters struct {
	Hosting     string `yaml:"hosting"`
	Storage     string `yaml:"storage"`
	Execution   string `yaml:"execution"`
	Game        string `yaml:"game"`
	Integration string `yaml:"integration"`
}

type ServerLifecycle struct {
	IdleShutdown                       string `yaml:"idle_shutdown"`
	RequireVerifiedBackupBeforeDestroy bool   `yaml:"require_verified_backup_before_destroy"`
	RequireApprovalForDestroy          bool   `yaml:"require_approval_for_destroy"`
}

// Load reads a single YAML document with unknown-field rejection, then checks
// cross-reference and safety rules. It never resolves secret references.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	value, err := Parse(file)
	if err != nil {
		return Config{}, fmt.Errorf("validate configuration: %w", err)
	}
	return value, nil
}

func Parse(reader io.Reader) (Config, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var value Config
	if err := decoder.Decode(&value); err != nil {
		// Decoder errors can include the offending scalar. Avoid returning raw
		// input so validation output cannot accidentally reveal a credential.
		return Config{}, fmt.Errorf("decode YAML failed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, fmt.Errorf("configuration must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("decode trailing YAML document failed")
	}
	if err := Validate(value); err != nil {
		return Config{}, err
	}
	return value, nil
}

func Validate(value Config) error {
	var problems []string
	add := func(ok bool, message string) {
		if !ok {
			problems = append(problems, message)
		}
	}
	add(value.SchemaVersion == SchemaVersion, "schema_version must be 1")
	add(value.Controller.Mode == "local", "controller.mode must be local")
	add(value.Controller.StateStore.Type == "bbolt", "controller.state_store.type must be bbolt")
	statePath := strings.TrimSpace(value.Controller.StateStore.Path)
	add(statePath != "" && !strings.ContainsRune(statePath, '\x00') && !strings.Contains(statePath, "://"), "controller.state_store.path must be a non-empty local path")
	timeout, err := time.ParseDuration(value.Controller.Defaults.OperationTimeout)
	add(err == nil && timeout > 0 && timeout <= 24*time.Hour, "controller.defaults.operation_timeout must be greater than zero and no more than 24h")
	add(value.Controller.Defaults.RetryLimit != nil && *value.Controller.Defaults.RetryLimit >= 0 && *value.Controller.Defaults.RetryLimit <= 10, "controller.defaults.retry_limit must be explicitly set between 0 and 10")
	add(value.Controller.Audit.Enabled, "controller.audit.enabled must be true")
	add(value.Controller.Audit.Destination == "local", "controller.audit.destination must be local")
	add(len(value.Adapters) > 0, "at least one adapter profile is required")
	add(len(value.Environments) > 0, "at least one environment is required")
	add(len(value.Servers) > 0, "at least one server profile is required")

	adapterByID := make(map[string]AdapterProfile, len(value.Adapters))
	for i, profile := range value.Adapters {
		label := fmt.Sprintf("adapters[%d]", i)
		add(adapterIDPattern.MatchString(profile.ID), label+".id must be a lower-case dotted adapter ID")
		add(validCategory(profile.Category), label+".category is unsupported")
		if expectedPrefix, ok := categoryPrefix(profile.Category); ok {
			add(strings.HasPrefix(profile.ID, expectedPrefix), label+".id must use the category prefix "+expectedPrefix)
		}
		if _, exists := adapterByID[profile.ID]; exists {
			problems = append(problems, "duplicate adapter ID: "+profile.ID)
		} else if profile.ID != "" {
			adapterByID[profile.ID] = profile
		}
		for name, reference := range profile.SecretRefs {
			validateSecretReference(&problems, label+".secret_refs."+name, name, reference)
		}
	}

	environmentByID := make(map[string]Environment, len(value.Environments))
	hasProduction := false
	for i, environment := range value.Environments {
		label := fmt.Sprintf("environments[%d]", i)
		add(identifierPattern.MatchString(environment.ID), label+".id must be a lower-case identifier")
		add(environment.Classification == "production" || environment.Classification == "staging" || environment.Classification == "testing", label+".classification must be production, staging, or testing")
		add(environment.ProductionBackupAccess == "none" || environment.ProductionBackupAccess == "read_only" || environment.ProductionBackupAccess == "read_write", label+".production_backup_access must be none, read_only, or read_write")
		if environment.Classification == "production" {
			hasProduction = true
			add(environment.ProductionBackupAccess == "read_write", label+" production backup access must be read_write")
		} else {
			add(environment.ProductionBackupAccess != "read_write", label+" staging/testing must not have production backup write access")
		}
		if _, exists := environmentByID[environment.ID]; exists {
			problems = append(problems, "duplicate environment ID: "+environment.ID)
		} else if environment.ID != "" {
			environmentByID[environment.ID] = environment
		}
	}
	add(hasProduction, "at least one production environment is required")

	serverIDs := make(map[string]struct{}, len(value.Servers))
	for i, server := range value.Servers {
		label := fmt.Sprintf("servers[%d]", i)
		add(identifierPattern.MatchString(server.ID), label+".id must be a lower-case identifier")
		if _, exists := serverIDs[server.ID]; exists {
			problems = append(problems, "duplicate server ID: "+server.ID)
		} else if server.ID != "" {
			serverIDs[server.ID] = struct{}{}
		}
		if _, exists := environmentByID[server.EnvironmentID]; !exists {
			problems = append(problems, label+" references unknown environment "+server.EnvironmentID)
		}
		checkServerAdapter(&problems, label+".adapters.hosting", server.Adapters.Hosting, "hosting", adapterByID, true)
		checkServerAdapter(&problems, label+".adapters.storage", server.Adapters.Storage, "storage", adapterByID, true)
		checkServerAdapter(&problems, label+".adapters.execution", server.Adapters.Execution, "execution", adapterByID, true)
		checkServerAdapter(&problems, label+".adapters.game", server.Adapters.Game, "game", adapterByID, true)
		checkServerAdapter(&problems, label+".adapters.integration", server.Adapters.Integration, "integration", adapterByID, false)
		if server.Lifecycle.IdleShutdown != "" {
			idle, err := time.ParseDuration(server.Lifecycle.IdleShutdown)
			add(err == nil && idle > 0 && idle <= 7*24*time.Hour, label+".lifecycle.idle_shutdown must be greater than zero and no more than 168h")
		}
		add(server.Lifecycle.RequireVerifiedBackupBeforeDestroy, label+".lifecycle.require_verified_backup_before_destroy must be true")
		add(server.Lifecycle.RequireApprovalForDestroy, label+".lifecycle.require_approval_for_destroy must be true")
	}
	validateStorageSecretIsolation(&problems, value.Servers, environmentByID, adapterByID)

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("configuration is invalid:\n - %s", strings.Join(problems, "\n - "))
	}
	return nil
}

func validateSecretReference(problems *[]string, label, name string, reference SecretReference) {
	if !secretNamePattern.MatchString(name) {
		*problems = append(*problems, label+" must use a lower-case secret name")
	}
	if !validSystemdCredentialReference(reference.SecretRef) {
		*problems = append(*problems, label+".secret_ref must reference a systemd credential as systemd-credential://NAME")
	}
	if strings.TrimSpace(reference.Purpose) == "" || len(reference.Purpose) > 100 {
		*problems = append(*problems, label+".purpose must be 1 to 100 characters")
	}
	if len(reference.VersionRef) > 500 {
		*problems = append(*problems, label+".version_ref must be at most 500 characters")
	}
	seen := make(map[string]struct{}, len(reference.PermissionScope))
	for _, scope := range reference.PermissionScope {
		if strings.TrimSpace(scope) == "" || len(scope) > 160 {
			*problems = append(*problems, label+".permission_scope entries must be 1 to 160 characters")
			continue
		}
		if _, duplicate := seen[scope]; duplicate {
			*problems = append(*problems, label+".permission_scope contains duplicate "+scope)
		}
		seen[scope] = struct{}{}
	}
}

func validSystemdCredentialReference(reference string) bool {
	if !strings.HasPrefix(reference, systemdCredentialScheme) {
		return false
	}
	return credentialNamePattern.MatchString(strings.TrimPrefix(reference, systemdCredentialScheme))
}

func validateStorageSecretIsolation(problems *[]string, servers []ServerProfile, environments map[string]Environment, adapters map[string]AdapterProfile) {
	uses := make(map[string]map[string]struct{})
	for _, server := range servers {
		environment, exists := environments[server.EnvironmentID]
		if !exists {
			continue
		}
		profile, exists := adapters[server.Adapters.Storage]
		if !exists || profile.Category != "storage" {
			continue
		}
		for _, reference := range profile.SecretRefs {
			if !validSystemdCredentialReference(reference.SecretRef) {
				continue
			}
			if uses[reference.SecretRef] == nil {
				uses[reference.SecretRef] = make(map[string]struct{})
			}
			uses[reference.SecretRef][environment.Classification] = struct{}{}
		}
	}
	for _, classifications := range uses {
		_, usedByProduction := classifications["production"]
		_, usedByStaging := classifications["staging"]
		_, usedByTesting := classifications["testing"]
		if usedByProduction && (usedByStaging || usedByTesting) {
			*problems = append(*problems, "storage secret references must not be shared between production and staging/testing servers")
			return
		}
	}
}

func checkServerAdapter(problems *[]string, label, id, expectedCategory string, profiles map[string]AdapterProfile, required bool) {
	if id == "" {
		if required {
			*problems = append(*problems, label+" is required")
		}
		return
	}
	profile, exists := profiles[id]
	if !exists {
		*problems = append(*problems, label+" references unknown adapter "+id)
		return
	}
	if profile.Category != expectedCategory {
		*problems = append(*problems, label+" must reference a "+expectedCategory+" adapter profile")
	}
}

func validCategory(category string) bool {
	_, ok := categoryPrefix(category)
	return ok
}

func categoryPrefix(category string) (string, bool) {
	switch category {
	case "hosting":
		return "hosting.", true
	case "storage":
		return "storage.", true
	case "execution":
		return "execution.", true
	case "game":
		return "game.", true
	case "integration":
		return "integration.", true
	default:
		return "", false
	}
}

func ResolveStateStorePath(configFile, configuredPath string) string {
	if filepath.IsAbs(configuredPath) {
		return filepath.Clean(configuredPath)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configFile), configuredPath))
}
