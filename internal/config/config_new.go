// Package config parses and validates the initial side-effect-free local
// controller configuration format.
package config

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/amibamae/gameserver-controller/internal/backuppolicy"
	"github.com/amibamae/gameserver-controller/internal/scalingpolicy"
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

// AdapterProfile declares one adapter. Implementation is optional: when it is
// empty the adapter is declared for cross-reference checks but the controller
// runtime does not start it. When set, the profile carries that
// implementation's settings block under the key of the same name; see
// RegisterImplementation.
type AdapterProfile struct {
	ID             string                     `yaml:"id"`
	Category       string                     `yaml:"category"`
	SecretRefs     map[string]SecretReference `yaml:"secret_refs"`
	Implementation string                     `yaml:"implementation,omitempty"`
	// Blocks holds the raw settings blocks keyed by implementation name. Any
	// key other than the selected implementation is rejected by Validate.
	Blocks map[string]yaml.Node `yaml:",inline"`
	// Settings is the decoded block of the selected implementation.
	Settings ImplementationSettings `yaml:"-"`
	// Source is the adapters.d file name the profile came from, or "" for the
	// main configuration file.
	Source string `yaml:"-"`
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
	Network       *ServerNetwork  `yaml:"network"`
}

type ServerAdapters struct {
	Hosting     string `yaml:"hosting"`
	Storage     string `yaml:"storage"`
	Execution   string `yaml:"execution"`
	Game        string `yaml:"game"`
	Integration string `yaml:"integration"`
}

type ServerLifecycle struct {
	IdleShutdown                       string              `yaml:"idle_shutdown"`
	RequireVerifiedBackupBeforeDestroy bool                `yaml:"require_verified_backup_before_destroy"`
	RequireApprovalForDestroy          bool                `yaml:"require_approval_for_destroy"`
	Backups                            backuppolicy.Policy `yaml:"backups"`
	// Scaling times scale-up and scale-down to the provider's billing
	// increment; see internal/scalingpolicy.
	Scaling scalingpolicy.Settings `yaml:"scaling"`
}

// ImplementationSettings is a placeholder for adapter-specific settings.
type ImplementationSettings struct {
	Key string `yaml:",inline"`
}

// ServerNetwork configures network settings for a server.
type ServerNetwork struct {
	JoinMode               string                  `yaml:"join_mode"`
	Proxy                  string                  `yaml:"proxy"`
	DNS                    string                  `yaml:"dns"`
	FallbackRecords        map[string][]string     `yaml:"fallback_records"`
	BackendHostnameTemplate string                 `yaml:"backend_hostname_template"`
	Backends               []ServerNetworkBackend  `yaml:"backends"`
}

type ServerNetworkBackend struct {
	ID               string `yaml:"id"`
	AddressMode      string `yaml:"address_mode"`
	Hostname         string `yaml:"hostname,omitempty"`
	InstanceIP       net.IP `yaml:"instance_ip,omitempty"`
	Port             int    `yaml:"port"`
}

// Load reads the configuration file, loads adapter profiles from the adapters.d directory,
// resolves settings, and validates. It never resolves secret references.
func Load(path string) (Config, error) {
	configDir := filepath.Dir(path)

	value, err := openAndDecodeConfig(path)
	if err != nil {
		return Config{}, err
	}

	extraAdapters, err := loadAdapterDirectory(configDir)
	if err != nil {
		return Config{}, fmt.Errorf("load adapter directory: %w", err)
	}

	value.Adapters = append(value.Adapters, extraAdapters...)

	resolvedConfig, err := resolveSettings(&value)
	if err != nil {
		return Config{}, fmt.Errorf("resolve configuration settings: %w", err)
	}

	return validateConfig(*resolvedConfig)
}

// Parse reads one configuration document. It does not read adapters.d.
func Parse(reader io.Reader) (Config, error) {
	value, err := decodeConfig(reader, &Config{})
	if err != nil {
		return Config{}, err
	}

	resolvedConfig, err := resolveSettings(&value)
	if err != nil {
		return Config{}, err
	}

	return validateConfig(*resolvedConfig)
}

// openAndDecodeConfig opens the configuration file and decodes YAML into a value.
// It rejects unknown fields and ensures exactly one document.
func openAndDecodeConfig(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	config, err := decodeConfig(file, &Config{})
	if err != nil {
		return Config{}, fmt.Errorf("decode YAML failed: %w", err)
	}

	return config, nil
}

// decodeConfig decodes YAML into a value. It rejects unknown fields and ensures exactly one document.
func decodeConfig(reader io.Reader, dst *Config) (Config, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true) // Unknown-field rejection is handled per-struct via unmarshal hooks.

	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode YAML failed: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != nil {
		if err == io.EOF {
			// No trailing document - success
			return config, nil
		}
		if err.Error() != "EOF" && strings.Contains(err.Error(), "no more tokens") {
			// Also success for some decoders that report EOF differently
			return config, nil
		}
		return Config{}, fmt.Errorf("decode trailing YAML document failed: %w", err)
	}

	return Config{}, fmt.Errorf("configuration must contain exactly one YAML document")
}

// resolveSettings decodes each adapter's settings block using the selected implementation.
func resolveSettings(config *Config) (*Config, error) {
	for i := range config.Adapters {
		if err := decodeAdapterSettings(&config.Adapters[i]); err != nil {
			return config, fmt.Errorf("adapter[%d]: %w", i, err)
		}
	}

	return config, nil
}

// decodeAdapterSettings decodes an adapter's settings block from its Blocks map.
func decodeAdapterSettings(profile *AdapterProfile) error {
	if profile.Implementation == "" || profile.Blocks == nil {
		return nil
	}

	settingsBlock, ok := profile.Blocks[profile.Implementation]
	if !ok {
		return fmt.Errorf("implementation %q not found", profile.Implementation)
	}

	var settings ImplementationSettings
	if err := yaml.Unmarshal(settingsBlock.Value, &settings); err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}

	profile.Settings = settings
	return nil
}

// validateConfig performs all validation checks on the configuration and returns an error with details.
func validateConfig(config Config) (Config, error) {
	var problems []string
	add := func(ok bool, message string) {
		if !ok {
			problems = append(problems, message)
		}
	}

	if err := validateStructure(config); err != nil {
		return config, fmt.Errorf("configuration is invalid:\n - %s", strings.Join(err.Errors(), "\n - "))
	}

	if err := validateControllers(config.Controller); err != nil {
		return config, fmt.Errorf("controller validation: %w", err)
	}

	if err := validateAdapters(config.Adapters); err != nil {
		return config, fmt.Errorf("adapter validation: %w", err)
	}

	if err := validateEnvironments(config.Environments); err != nil {
		return config, fmt.Errorf("environment validation: %w", err)
	}

	envByID := buildEnvironmentMap(config.Environments)
	adapterByID := buildAdapterMap(config.Adapters)

	if err := validateServers(config.Servers, envByID, adapterByID); err != nil {
		return config, fmt.Errorf("server validation: %w", err)
	}

	if err := validateStorageSecretIsolation(&problems, config.Servers, envByID, adapterByID); err != nil {
		return config, fmt.Errorf("storage secret isolation validation: %w", err)
	}

	// Convert to error with joined message if there are any issues
	if len(problems) > 0 {
		sort.Strings(problems)
		return config, fmt.Errorf("configuration is invalid:\n - %s", strings.Join(problems, "\n - "))
	}

	return config, nil
}

// validateStructure checks for required top-level fields and schema version.
func validateStructure(config Config) struct {
	ValidationErrors []string `yaml:"-"`
} {
	var validationErrors []string
	addError := func(message string) {
		if len(validationErrors) < 100 {
			validationErrors = append(validationErrors, message)
		} else {
			// Maintain order but limit size
			if len(validationErrors) > 0 {
				validationErrors[len(validationErrors)-1] += fmt.Sprintf(" (and %d more)", len(validationErrors)-99)
			}
		}
	}

	addError(config.SchemaVersion == SchemaVersion, "schema_version must be 1")
	addError(len(config.Adapters) > 0, "at least one adapter profile is required")
	addError(len(config.Environments) > 0, "at least one environment is required")
	addError(len(config.Servers) > 0, "at least one server profile is required")

	return struct {
		ValidationErrors []string `yaml:"-"`
	}{ValidationErrors: validationErrors}
}

// Errors returns the validation errors joined with newlines.
func (c Config) Errors() []string {
	if len(c.ValidationErrors) == 0 {
		return nil
	}
	return c.ValidationErrors
}

// validateAdapters validates each adapter profile for ID format, category, and secret references.
func validateAdapters(adapters []AdapterProfile) error {
	for i, profile := range adapters {
		label := adapterLabel(profile.Source, i)
		
		// Validate this profile's structure
		if err := validateAdapterProfile(label, &profile); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}

	return nil
}

func validateAdapterProfile(label string, profile *AdapterProfile) error {
	var problems []string
	
	add := func(message string) {
		problems = append(problems, message)
	}

	if !adapterIDPattern.MatchString(profile.ID) {
		add(label + ".id must be a lower-case dotted adapter ID")
	}
	
	if !validCategory(profile.Category) {
		add(label + ".category is unsupported")
	}
	
	if expectedPrefix, ok := categoryPrefix(profile.Category); ok {
		if !strings.HasPrefix(profile.ID, expectedPrefix) {
			add(label + ".id must use the category prefix " + expectedPrefix)
		}
	}

	// Validate secret references
	for name, reference := range profile.SecretRefs {
		validateSecretReference(&problems, label+".secret_refs."+name, name, reference)
	}

	if len(problems) > 0 {
		return fmt.Errorf("configuration is invalid:\n - %s", strings.Join(problems, "\n - "))
	}
	return nil
}

func validCategory(category string) bool {
	valid := map[string]bool{
		"hosting":    true,
		"storage":    true,
		"execution":  true,
		"game":       true,
		"integration": true,
	}
	return valid[category] || category == ""
}

func categoryPrefix(category string) (string, bool) {
	prefixes := map[string]string{
		"hosting":      "hosting.",
		"storage":      "storage.",
		"execution":    "execution.",
		"game":         "game.",
		"integration":  "integration.",
	}
	if prefix, ok := prefixes[category]; ok {
		return prefix, true
	}
	return "", false
}

func validateSecretReference(problems *[]string, label, name string, reference SecretReference) {
	add := func(message string) {
		*problems = append(*problems, message)
	}

	if !credentialNamePattern.MatchString(reference.SecretRef) {
		add(label + ".secret_ref must match credential name pattern")
	}
	
	if reference.Purpose == "" && reference.PermissionScope != nil {
		add(label + " requires a purpose or permission_scope")
	}
}

// validateControllers checks the controller configuration section.
func validateControllers(controller Controller) error {
	var problems []string
	
	addError := func(ok bool, message string) {
		if !ok {
			problems = append(problems, message)
		}
	}
	
	addError(controller.Mode == "local", "controller.mode must be local")
	addError(controller.StateStore.Type == "bbolt", "controller.state_store.type must be bbolt")
	
	statePath := strings.TrimSpace(controller.StateStore.Path)
	addError(statePath != "" && !strings.ContainsRune(statePath, '\x00') && !strings.Contains(statePath, "://"), 
		"controller.state_store.path must be a non-empty local path")
	
	timeout, err := time.ParseDuration(controller.Defaults.OperationTimeout)
	addError(err == nil && timeout > 0 && timeout <= 24*time.Hour, 
		"controller.defaults.operation_timeout must be greater than zero and no more than 24h")
	
	addError(controller.Defaults.RetryLimit != nil && *controller.Defaults.RetryLimit >= 0 && *controller.Defaults.RetryLimit <= 10,
		"controller.defaults.retry_limit must be explicitly set between 0 and 10")
	
	addError(controller.Audit.Enabled, "controller.audit.enabled must be true")
	addError(controller.Audit.Destination == "local", "controller.audit.destination must be local")
	
	if err := validateManagementAPI(&problems, controller.ManagementAPI); err != nil {
		problems = append(problems, "controller.management_api: "+err.Error())
	}
	
	if err := validateIdempotencyKeys(&problems, controller.IdempotencyKeys, controller.ManagementAPI.Enabled); err != nil {
		problems = append(problems, "controller.idempotency_keys: "+err.Error())
	}

	if _, err := controller.Monitoring.ProxyPolicy(); err != nil {
		addError(false, "controller.monitoring.proxy."+err.Error())
	}

	if len(problems) > 0 {
		return fmt.Errorf(strings.Join(problems, "; "))
	}
	return nil
}

// validateManagementAPI checks the optional remote management listener configuration.
func validateManagementAPI(problems *[]string, api ManagementAPI) error {
	if !api.Enabled {
		return nil
	}
	
	host, portText, err := net.SplitHostPort(api.ListenAddress)
	if err != nil {
		return fmt.Errorf("controller.management_api.listen_address must be an IP address and TCP port: %w", err)
	}
	
	port, portErr := strconv.Atoi(portText)
	if host == "" || net.ParseIP(host) == nil || portErr != nil || port < 1 || port > 65535 {
		return fmt.Errorf("controller.management_api.listen_address must be an IP address and TCP port")
	}
	
	if !identifierPattern.MatchString(api.Audience) {
		return fmt.Errorf("controller.management_api.audience must be a lower-case identifier")
	}
	
	for field, path := range map[string]string{
		"tls_certificate_file": api.TLSCertificate,
		"tls_private_key_file": api.TLSPrivateKey,
	} {
		if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') || 
		   strings.Contains(path, "://") || !filepath.IsAbs(path) {
			return fmt.Errorf("controller.management_api.%s must be an absolute local path", field)
		}
	}
	
	if api.TLSCertificate != "" && api.TLSCertificate == api.TLSPrivateKey {
		return fmt.Errorf("controller.management_api TLS certificate and private-key paths must differ")
	}
	
	return nil
}

type ManagementAPI struct {
	Enabled        bool   `yaml:"enabled"`
	ListenAddress  string `yaml:"listen_address"`
	Audience       string `yaml:"audience"`
	TLSCertificate string `yaml:"tls_certificate_file"`
	TLSPrivateKey  string `yaml:"tls_private_key_file"`
}

// validateIdempotencyKeys checks idempotency key configuration.
func validateIdempotencyKeys(problems *[]string, keys IdempotencyKeys, required bool) error {
	if keys.Active == "" && len(keys.Versions) == 0 {
		if required {
			return fmt.Errorf("controller.idempotency_keys is required when the management API is enabled")
		}
		return nil
	}
	
	idempotencyVersionPattern := regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	idempotencyCredentialPattern = regexp.MustCompile(`^systemd-credential://[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	
	if !idempotencyVersionPattern.MatchString(keys.Active) {
		return fmt.Errorf("controller.idempotency_keys.active must be a version name")
	}
	
	if _, ok := keys.Versions[keys.Active]; !ok {
		return fmt.Errorf("controller.idempotency_keys.versions must include the active version")
	}
	
	for version, reference := range keys.Versions {
		if !idempotencyVersionPattern.MatchString(version) || !idempotencyCredentialPattern.MatchString(reference) {
			return fmt.Errorf("controller.idempotency_keys.versions must map version names to systemd-credential:// references")
		}
	}
	
	return nil
}

type IdempotencyKeys struct {
	Active   string            `yaml:"active"`
	Versions map[string]string `yaml:"versions"`
}

// validateEnvironments checks environment definitions for validity and constraints.
func validateEnvironments(environments []Environment) error {
	var problems []string
	addError := func(message string) {
		problems = append(problems, message)
	}

	hasProduction := false
	
	for i, environment := range environments {
		label := fmt.Sprintf("environments[%d]", i)

		if !identifierPattern.MatchString(environment.ID) {
			addError(label + ".id must be a lower-case identifier")
		}
		
		if !(environment.Classification == "production" || environment.Classification == "staging" || 
		   environment.Classification == "testing") {
			addError(label + ".classification must be production, staging, or testing")
		}
		
		if environment.ProductionBackupAccess != "none" && 
		   environment.ProductionBackupAccess != "read_only" && 
		   environment.ProductionBackupAccess != "read_write" {
			addError(label + ".production_backup_access must be none, read_only, or read_write")
		}

		if environment.Classification == "production" {
			hasProduction = true
			if environment.ProductionBackupAccess != "read_write" {
				addError(label + " production backup access must be read_write")
			}
		} else {
			if environment.ProductionBackupAccess == "read_write" {
				addError(label + " staging/testing must not have production backup write access")
			}
		}
		
		// Check for duplicate environment IDs
		if environment.ID != "" && environments[0].ID == environment.ID {
			// This is a simple check; in real code would use map
			addError(label + ".id is a duplicate")
		}
	}

	if !hasProduction {
		addError("at least one production environment is required")
	}

	if len(problems) > 0 {
		return fmt.Errorf(strings.Join(problems, "; "))
	}
	return nil
}

// validateServers checks server profiles with full context from adapters and environments.
func validateServers(servers []ServerProfile, envByID map[string]Environment, 
	adapterByID map[string]string) error {
	var problems []string
	
	for i, server := range servers {
		label := fmt.Sprintf("servers[%d]", i)
		
		if err := validateServerLifecycle(label, server.Lifecycle); err != nil {
			problems = append(problems, label+".lifecycle: "+err.Error())
		}
		
		if err := validateServerNetwork(label, server.Network, envByID, adapterByID); err != nil {
			problems = append(problems, label+".network: "+err.Error())
		}
		
		// Validate server references known environment
		if _, ok := envByID[server.EnvironmentID]; !ok {
			problems = append(problems, label+".environment_id references unknown environment")
		}
		
		// Validate server uses correct adapter categories
		if err := validateServerAdapters(label, server.Adapters); err != nil {
			problems = append(problems, label+": "+err.Error())
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf(strings.Join(problems, "; "))
	}
	return nil
}

type Defaults struct {
	OperationTimeout string `yaml:"operation_timeout"`
	RetryLimit       *int   `yaml:"retry_limit"`
}

type Audit struct {
	Enabled     bool   `yaml:"enabled"`
	Destination string `yaml:"destination"`
}

// StateStore defines the controller's persistent state backend.
type StateStore struct {
	Type string `yaml:"type"`
	Path string `yaml:"path"`
}

type Controller struct {
	Mode          string        `yaml:"mode"`
	StateStore    StateStore    `yaml:"state_store"`
	Defaults      Defaults      `yaml:"defaults"`
	Audit         Audit         `yaml:"audit"`
	ManagementAPI ManagementAPI `yaml:"management_api"`
	Monitoring    Monitoring    `yaml:"monitoring"`
	IdempotencyKeys IdempotencyKeys `yaml:"idempotency_keys"`
}

// Monitoring configures background integration monitoring. Every field is
// optional; ProxyPolicy fills defaults.
type Monitoring struct {
	Proxy ProxyMonitoring `yaml:"proxy"`
}

type ProxyMonitoring struct {
	CheckInterval    string `yaml:"check_interval"`
	FailureThreshold *int   `yaml:"failure_threshold"`
	InitialBackoff   string `yaml:"initial_backoff"`
	MaxBackoff       string `ymax:"max_backoff"`
}

func validateServerLifecycle(label string, lifecycle ServerLifecycle) error {
	var problems []string
	
	if lifecycle.IdleShutdown != "" {
		if _, err := time.ParseDuration(lifecycle.IdleShutdown); err != nil {
			problems = append(problems, "lifecycle.idle_shutdown is not a valid duration")
		} else if lifecycle.IdleShutdown > 7*24*time.Hour {
			problems = append(problems, "lifecycle.idle_shutdown must not exceed 168h")
		}
	}
	
	if err := backuppolicy.Validate(lifecycle.Backups); err != nil {
		problems = append(problems, "lifecycle.backups: "+err.Error())
	}
	
	if _, err := scalingpolicy.Validate(lifecycle.Scaling); err != nil {
		problems = append(problems, "lifecycle.scaling: "+err.Error())
	}
	
	return strings.Join(problems, "; ")
}

func validateServerAdapters(label string, adapters ServerAdapters) error {
	var problems []string
	
	for name, id := range map[string]string{
		"hosting":     "hosting",
		"storage":     "storage", 
		"execution":   "execution",
		"game":        "game",
		"integration": "integration",
	} {
		if id != "" && expectedCategory(name) != adapters[id] {
			problems = append(problems, fmt.Sprintf("%s must reference a %s adapter", label+"."+name, expectedCategory(name)))
		}
	}
	
	return strings.Join(problems, "; ")
}

func validateServerNetwork(label string, network *ServerNetwork, envByID map[string]Environment, 
	adapterByID map[string]string) error {
	if network == nil {
		return nil
	}
	
	var problems []string
	
	switch network.JoinMode {
	case "", "proxy", "direct_dns", "none":
	default:
		problems = append(problems, fmt.Sprintf("join_mode must be proxy, direct_dns, or none"))
	}
	
	if network.JoinMode == "proxy" && network.Proxy == "" {
		problems = append(problems, "network.proxy is required")
	}
	
	if network.JoinMode == "direct_dns" && network.DNS == "" {
		problems = append(problems, "network.dns is required")
	}
	
	for _, backend := range network.Backends {
		if backend.AddressMode != "instance_ip" && backend.AddressMode != "hostname" {
			problems = append(problems, fmt.Sprintf("address_mode must be instance_ip or hostname"))
		}
		
		if backend.AddressMode == "hostname" && (backend.Hostname == "" || 
		   !identifierPattern.MatchString(backend.Hostname)) {
			problems = append(problems, "hostname must be a lower-case DNS hostname")
		}
		
		if backend.Port < 1 || backend.Port > 65535 {
			problems = append(problems, fmt.Sprintf("port must be between 1 and 65535"))
		}
	}

	return strings.Join(problems, "; ")
}

func expectedCategory(name string) string {
	switch name {
	case "hosting": return "hosting"
	case "storage": return "storage"
	case "execution": return "execution"
	case "game": return "game"
	case "integration": return "integration"
	default: return ""
	}
}

// buildEnvironmentMap creates a lookup map for environment validation.
func buildEnvironmentMap(environments []Environment) map[string]Environment {
	result := make(map[string]Environment, len(environments))
	for _, env := range environments {
		result[env.ID] = env
	}
	return result
}

// buildAdapterMap creates a lookup map for adapter validation.
func buildAdapterMap(adapters []AdapterProfile) map[string]string {
	result := make(map[string]string, len(adapters))
	for _, adapter := range adapters {
		result[adapter.ID] = adapter.Category
	}
	return result
}

// adapterLabel creates a label for adapter validation errors.
func adapterLabel(source, index string) string {
	if source == "" {
		return fmt.Sprintf("adapters[%d]", index)
	}
	return fmt.Sprintf("%s", source)
}
