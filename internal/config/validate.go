package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/backuppolicy"
	"github.com/amibamae/gameserver-controller/internal/scalingpolicy"
)

func Validate(value Config) error {
	var problems []string
	requireConfig(&problems, value.SchemaVersion == SchemaVersion, "schema_version must be 1")
	validateController(&problems, value.Controller)
	requireConfig(&problems, len(value.Adapters) > 0, "at least one adapter profile is required")
	requireConfig(&problems, len(value.Environments) > 0, "at least one environment is required")
	requireConfig(&problems, len(value.Servers) > 0, "at least one server profile is required")

	adapterByID := validateAdapterProfiles(&problems, value.Adapters)
	environmentByID := validateEnvironmentProfiles(&problems, value.Environments)
	for i, profile := range value.Adapters {
		validateImplementation(&problems, profileLabel(profile.Source, i), profile, environmentByID)
	}
	validateServerProfiles(&problems, value.Servers, environmentByID, adapterByID)
	validateStorageSecretIsolation(&problems, value.Servers, environmentByID, adapterByID)
	validateUniqueBackendHostnames(&problems, value.Servers)
	validateProxyBackendIDs(&problems, value.Servers)

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("configuration is invalid:\n - %s", strings.Join(problems, "\n - "))
	}
	return nil
}

func validateServerProfiles(problems *[]string, servers []ServerProfile, environmentByID map[string]Environment, adapterByID map[string]AdapterProfile) {
	serverIDs := make(map[string]struct{}, len(servers))
	for i, server := range servers {
		label := fmt.Sprintf("servers[%d]", i)
		requireConfig(problems, identifierPattern.MatchString(server.ID), label+".id must be a lower-case identifier")
		if _, exists := serverIDs[server.ID]; exists {
			*problems = append(*problems, "duplicate server ID: "+server.ID)
		} else if server.ID != "" {
			serverIDs[server.ID] = struct{}{}
		}
		if _, exists := environmentByID[server.EnvironmentID]; !exists {
			*problems = append(*problems, label+" references unknown environment "+server.EnvironmentID)
		}
		checkServerAdapter(problems, label+".adapters.hosting", server.Adapters.Hosting, "hosting", adapterByID, true)
		checkServerAdapter(problems, label+".adapters.storage", server.Adapters.Storage, "storage", adapterByID, true)
		for field, id := range map[string]string{"hosting": server.Adapters.Hosting, "storage": server.Adapters.Storage, "execution": server.Adapters.Execution,
			"game": server.Adapters.Game, "integration": server.Adapters.Integration} {
			if environment := BoundEnvironment(adapterByID[id]); id != "" && environment != "" && environment != server.EnvironmentID {
				*problems = append(*problems, label+".adapters."+field+" must use an adapter bound to the same environment as the server")
			}
		}
		checkServerAdapter(problems, label+".adapters.execution", server.Adapters.Execution, "execution", adapterByID, true)
		checkServerAdapter(problems, label+".adapters.game", server.Adapters.Game, "game", adapterByID, true)
		checkServerAdapter(problems, label+".adapters.integration", server.Adapters.Integration, "integration", adapterByID, false)
		validateServerNetwork(problems, label, server, adapterByID)
		validateServerLifecycleRules(problems, label, server.Lifecycle)
	}
}

func validateEnvironmentProfiles(problems *[]string, environments []Environment) map[string]Environment {
	byID := make(map[string]Environment, len(environments))
	hasProduction := false
	for i, environment := range environments {
		label := fmt.Sprintf("environments[%d]", i)
		requireConfig(problems, identifierPattern.MatchString(environment.ID), label+".id must be a lower-case identifier")
		requireConfig(problems, environment.Classification == "production" || environment.Classification == "staging" || environment.Classification == "testing", label+".classification must be production, staging, or testing")
		requireConfig(problems, environment.ProductionBackupAccess == "none" || environment.ProductionBackupAccess == "read_only" || environment.ProductionBackupAccess == "read_write", label+".production_backup_access must be none, read_only, or read_write")
		if environment.Classification == "production" {
			hasProduction = true
			requireConfig(problems, environment.ProductionBackupAccess == "read_write", label+" production backup access must be read_write")
		} else {
			requireConfig(problems, environment.ProductionBackupAccess != "read_write", label+" staging/testing must not have production backup write access")
		}
		if _, exists := byID[environment.ID]; exists {
			*problems = append(*problems, "duplicate environment ID: "+environment.ID)
		} else if environment.ID != "" {
			byID[environment.ID] = environment
		}
	}
	requireConfig(problems, hasProduction, "at least one production environment is required")
	return byID
}

func validateAdapterProfiles(problems *[]string, profiles []AdapterProfile) map[string]AdapterProfile {
	byID := make(map[string]AdapterProfile, len(profiles))
	for i, profile := range profiles {
		label := profileLabel(profile.Source, i)
		// Direct Validate callers may supply undecoded settings. Implementation validation reports decoding errors.
		_ = resolveSettings(&profile)
		requireConfig(problems, adapterIDPattern.MatchString(profile.ID), label+".id must be a lower-case dotted adapter ID")
		requireConfig(problems, validCategory(profile.Category), label+".category is unsupported")
		if prefix, ok := categoryPrefix(profile.Category); ok {
			requireConfig(problems, strings.HasPrefix(profile.ID, prefix), label+".id must use the category prefix "+prefix)
		}
		if _, exists := byID[profile.ID]; exists {
			*problems = append(*problems, "duplicate adapter ID: "+profile.ID)
		} else if profile.ID != "" {
			byID[profile.ID] = profile
		}
		for name, reference := range profile.SecretRefs {
			validateSecretReference(problems, label+".secret_refs."+name, name, reference)
		}
	}
	return byID
}

func validateServerLifecycleRules(problems *[]string, label string, lifecycle ServerLifecycle) {
	if lifecycle.IdleShutdown != "" {
		idle, err := time.ParseDuration(lifecycle.IdleShutdown)
		requireConfig(problems, err == nil && idle > 0 && idle <= 7*24*time.Hour, label+".lifecycle.idle_shutdown must be greater than zero and no more than 168h")
	}
	if _, err := backuppolicy.Resolve(lifecycle.Backups); err != nil {
		*problems = append(*problems, label+".lifecycle.backups: "+err.Error())
	}
	if _, err := scalingpolicy.Resolve(lifecycle.Scaling); err != nil {
		*problems = append(*problems, label+".lifecycle.scaling: "+err.Error())
	}
	requireConfig(problems, lifecycle.RequireVerifiedBackupBeforeDestroy, label+".lifecycle.require_verified_backup_before_destroy must be true")
	requireConfig(problems, lifecycle.RequireApprovalForDestroy, label+".lifecycle.require_approval_for_destroy must be true")
}

func requireConfig(problems *[]string, ok bool, message string) {
	if !ok {
		*problems = append(*problems, message)
	}
}
