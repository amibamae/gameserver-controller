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
	"strconv"
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

type Controller struct {
	Mode          string        `yaml:"mode"`
	StateStore    StateStore    `yaml:"state_store"`
	Defaults      Defaults      `yaml:"defaults"`
	Audit         Audit         `yaml:"audit"`
	ManagementAPI ManagementAPI `yaml:"management_api"`
}

// ManagementAPI configures the optional remote HTTPS management listener.
// Disabled is the zero value, so existing configurations remain offline.
type ManagementAPI struct {
	Enabled        bool   `yaml:"enabled"`
	ListenAddress  string `yaml:"listen_address"`
	Audience       string `yaml:"audience"`
	TLSCertificate string `yaml:"tls_certificate_file"`
	TLSPrivateKey  string `yaml:"tls_private_key_file"`
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
	Network       *ServerNetwork  `yaml:"network"`
}

// ServerNetwork is a server's network profile: how players reach it and
// which integration adapters that requires. See
// protocol/adapters/integrations.md section 6. A missing block means join
// mode "none".
type ServerNetwork struct {
	// JoinMode is "proxy", "direct_dns", or "none".
	JoinMode string `yaml:"join_mode"`
	// Proxy is the proxy integration adapter; required for join mode proxy.
	Proxy string `yaml:"proxy"`
	// DNS is the DNS integration adapter; required for join mode direct_dns
	// and for any backend that uses a managed hostname.
	DNS string `yaml:"dns"`
	// JoinHostname is the name players join through in direct_dns mode.
	JoinHostname string `yaml:"join_hostname"`
	// BackendHostnameTemplate gives every hostname backend its own DNS name
	// from one scheme, such as "{backend}.{environment}.mc.example.com".
	// Placeholders: {backend} (required), {server}, and {environment}.
	BackendHostnameTemplate string `yaml:"backend_hostname_template"`
	// Backends are the proxy backends for this server (proxy mode only).
	Backends []NetworkBackend `yaml:"backends"`
}

// NetworkBackend says how the proxy reaches one backend.
type NetworkBackend struct {
	ID string `yaml:"id"`
	// AddressMode is "instance_ip" (the instance's current public IP) or
	// "hostname" (a managed DNS name kept pointed at the instance). It
	// defaults to "hostname" when the profile sets a hostname template.
	AddressMode string `yaml:"address_mode"`
	Hostname    string `yaml:"hostname"`
	Port        int    `yaml:"port"`
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
	validateManagementAPI(&problems, value.Controller.ManagementAPI)
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
		validateServerNetwork(&problems, label, server, adapterByID)
		if server.Lifecycle.IdleShutdown != "" {
			idle, err := time.ParseDuration(server.Lifecycle.IdleShutdown)
			add(err == nil && idle > 0 && idle <= 7*24*time.Hour, label+".lifecycle.idle_shutdown must be greater than zero and no more than 168h")
		}
		if _, err := backuppolicy.Resolve(server.Lifecycle.Backups); err != nil {
			problems = append(problems, label+".lifecycle.backups: "+err.Error())
		}
		if _, err := scalingpolicy.Resolve(server.Lifecycle.Scaling); err != nil {
			problems = append(problems, label+".lifecycle.scaling: "+err.Error())
		}
		add(server.Lifecycle.RequireVerifiedBackupBeforeDestroy, label+".lifecycle.require_verified_backup_before_destroy must be true")
		add(server.Lifecycle.RequireApprovalForDestroy, label+".lifecycle.require_approval_for_destroy must be true")
	}
	validateStorageSecretIsolation(&problems, value.Servers, environmentByID, adapterByID)
	validateUniqueBackendHostnames(&problems, value.Servers)

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("configuration is invalid:\n - %s", strings.Join(problems, "\n - "))
	}
	return nil
}

func validateManagementAPI(problems *[]string, value ManagementAPI) {
	if !value.Enabled {
		return
	}
	host, portText, err := net.SplitHostPort(value.ListenAddress)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host == "" || net.ParseIP(host) == nil || portErr != nil || port < 1 || port > 65535 {
		*problems = append(*problems, "controller.management_api.listen_address must be an IP address and TCP port")
	}
	if !identifierPattern.MatchString(value.Audience) {
		*problems = append(*problems, "controller.management_api.audience must be a lower-case identifier")
	}
	for field, path := range map[string]string{
		"tls_certificate_file": value.TLSCertificate,
		"tls_private_key_file": value.TLSPrivateKey,
	} {
		if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') || strings.Contains(path, "://") || !filepath.IsAbs(path) {
			*problems = append(*problems, "controller.management_api."+field+" must be an absolute local path")
		}
	}
	if value.TLSCertificate != "" && value.TLSCertificate == value.TLSPrivateKey {
		*problems = append(*problems, "controller.management_api TLS certificate and private-key paths must differ")
	}
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

var (
	backendIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	dnsHostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	numericHostPattern = regexp.MustCompile(`^[0-9.]+$`)
)

var templatePlaceholder = regexp.MustCompile(`\{[^}]*\}`)

// BackendHostname returns the DNS name for a hostname-mode backend: its
// explicit hostname, or the profile's template filled in for this server.
func (n ServerNetwork) BackendHostname(serverID, environmentID string, backend NetworkBackend) (string, bool) {
	if backend.effectiveAddressMode(n) != "hostname" {
		return "", false
	}
	if n.BackendHostnameTemplate == "" {
		return backend.Hostname, backend.Hostname != ""
	}
	name := strings.NewReplacer("{backend}", backend.ID, "{server}", serverID, "{environment}", environmentID).Replace(n.BackendHostnameTemplate)
	return name, true
}

func (b NetworkBackend) effectiveAddressMode(n ServerNetwork) string {
	if b.AddressMode == "" && n.BackendHostnameTemplate != "" {
		return "hostname"
	}
	return b.AddressMode
}

func validDNSHostname(value string) bool {
	return len(value) <= 253 && dnsHostnamePattern.MatchString(value) && !numericHostPattern.MatchString(value)
}

// validateServerNetwork checks a server's network profile and that every
// integration adapter it requires is configured.
func validateServerNetwork(problems *[]string, label string, server ServerProfile, adapters map[string]AdapterProfile) {
	network := server.Network
	if network == nil {
		return
	}
	label += ".network"
	add := func(ok bool, problem string) {
		if !ok {
			*problems = append(*problems, label+problem)
		}
	}
	usesHostname := false
	for _, backend := range network.Backends {
		usesHostname = usesHostname || backend.effectiveAddressMode(*network) == "hostname"
	}
	if template := network.BackendHostnameTemplate; template != "" {
		placeholdersOK := strings.Contains(template, "{backend}")
		for _, placeholder := range templatePlaceholder.FindAllString(template, -1) {
			placeholdersOK = placeholdersOK && (placeholder == "{backend}" || placeholder == "{server}" || placeholder == "{environment}")
		}
		add(placeholdersOK, ".backend_hostname_template must contain {backend} and may only use {backend}, {server}, and {environment}")
		add(network.JoinMode == "proxy", ".backend_hostname_template is only used with join_mode proxy")
	}
	switch network.JoinMode {
	case "proxy":
		checkServerAdapter(problems, label+".proxy", network.Proxy, "integration", adapters, true)
		add(len(network.Backends) > 0, ".backends must list at least one backend for join_mode proxy")
		add(network.JoinHostname == "", ".join_hostname is only used with join_mode direct_dns")
		if legacy := server.Adapters.Integration; legacy != "" {
			add(legacy == network.Proxy, ".proxy must match adapters.integration when both are set")
		}
	case "direct_dns":
		add(network.Proxy == "", ".proxy is only used with join_mode proxy")
		add(len(network.Backends) == 0, ".backends are only used with join_mode proxy")
		add(validDNSHostname(network.JoinHostname), ".join_hostname must be a lower-case DNS hostname")
	case "none":
		add(network.Proxy == "" && network.DNS == "" && network.JoinHostname == "" && len(network.Backends) == 0,
			" with join_mode none must not set proxy, dns, join_hostname, or backends")
		return
	default:
		add(false, ".join_mode must be proxy, direct_dns, or none")
		return
	}
	checkServerAdapter(problems, label+".dns", network.DNS, "integration", adapters, network.JoinMode == "direct_dns" || usesHostname)
	if network.JoinMode == "proxy" && !usesHostname {
		add(network.DNS == "", ".dns is only used with join_mode direct_dns or hostname backends")
	}
	seen := make(map[string]bool, len(network.Backends))
	for i, backend := range network.Backends {
		entry := fmt.Sprintf(".backends[%d]", i)
		add(backendIDPattern.MatchString(backend.ID), entry+".id must be a proxy server name")
		add(!seen[backend.ID], entry+".id is a duplicate")
		seen[backend.ID] = true
		add(backend.Port >= 1 && backend.Port <= 65535, entry+".port must be between 1 and 65535")
		switch backend.effectiveAddressMode(*network) {
		case "instance_ip":
			add(backend.Hostname == "", entry+".hostname is only used with address_mode hostname")
		case "hostname":
			if network.BackendHostnameTemplate != "" {
				add(backend.Hostname == "", entry+".hostname must not be set when backend_hostname_template is set")
				name, _ := network.BackendHostname(server.ID, server.EnvironmentID, backend)
				add(validDNSHostname(name), entry+" hostname from backend_hostname_template is not a valid lower-case DNS hostname")
			} else {
				add(validDNSHostname(backend.Hostname), entry+".hostname must be a lower-case DNS hostname")
			}
		default:
			add(false, entry+".address_mode must be instance_ip or hostname")
		}
	}
}

// validateUniqueBackendHostnames requires every backend hostname to belong to
// exactly one backend, so each backend server has its own domain.
func validateUniqueBackendHostnames(problems *[]string, servers []ServerProfile) {
	owners := map[string]string{}
	for i, server := range servers {
		if server.Network == nil {
			continue
		}
		for j, backend := range server.Network.Backends {
			name, ok := server.Network.BackendHostname(server.ID, server.EnvironmentID, backend)
			if !ok || name == "" {
				continue
			}
			label := fmt.Sprintf("servers[%d].network.backends[%d]", i, j)
			if owner, taken := owners[name]; taken {
				*problems = append(*problems, label+" hostname is already used by "+owner)
				continue
			}
			owners[name] = label
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
