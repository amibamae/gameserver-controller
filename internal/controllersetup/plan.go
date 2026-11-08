// Package controllersetup builds a read-only installation plan for an
// operator-owned controller machine.
package controllersetup

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/config"
)

// Layout contains the fixed paths used by the first systemd installation.
// These are values for preview output only; Plan never creates or changes them.
type Layout struct {
	ConfigFile        string `json:"config_file"`
	ServiceFile       string `json:"service_file"`
	StateDir          string `json:"state_dir"`
	StateDatabaseFile string `json:"state_database_file"`
	CredentialDir     string `json:"credential_dir"`
}

// Host describes facts collected without changing the target machine.
type Host struct {
	OSReleaseID string `json:"os_release_id"`
	Systemd     bool   `json:"systemd_available"`
}

// Plan is a safe, redacted preview. It intentionally excludes credential
// reference values and all credential contents.
type Plan struct {
	Target             string   `json:"target"`
	Host               Host     `json:"host"`
	Configuration      Summary  `json:"configuration"`
	CredentialRefCount int      `json:"credential_reference_count"`
	Layout             Layout   `json:"layout"`
	Actions            []string `json:"actions"`
	Blockers           []string `json:"blockers"`
	TargetSupported    bool     `json:"target_supported"`
}

// Summary reports only non-secret configuration counts.
type Summary struct {
	SchemaVersion int `json:"schema_version"`
	Adapters      int `json:"adapters"`
	Environments  int `json:"environments"`
	Servers       int `json:"servers"`
}

// BuildPlan validates configuration and combines it with inspected host facts.
// It does not read credential files, access the network, or make changes.
func BuildPlan(value config.Config, host Host, layout Layout) (Plan, error) {
	if err := config.Validate(value); err != nil {
		return Plan{}, fmt.Errorf("validate controller configuration")
	}
	for name, path := range map[string]string{
		"config file":          layout.ConfigFile,
		"service file":         layout.ServiceFile,
		"state directory":      layout.StateDir,
		"credential directory": layout.CredentialDir,
	} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
			return Plan{}, fmt.Errorf("%s must be an absolute path", name)
		}
	}
	databasePath, err := resolveStateDatabase(layout.StateDir, value.Controller.StateStore.Path)
	if err != nil {
		return Plan{}, err
	}
	layout.StateDatabaseFile = databasePath
	plan := Plan{
		Target: "existing-machine",
		Host:   host,
		Configuration: Summary{
			SchemaVersion: value.SchemaVersion,
			Adapters:      len(value.Adapters),
			Environments:  len(value.Environments),
			Servers:       len(value.Servers),
		},
		CredentialRefCount: countCredentialRefs(value),
		Layout:             layout,
		Actions: []string{
			"create dedicated controller service account",
			"install controller configuration with private permissions",
			"prepare named encrypted systemd credentials from protected operator input",
			"install the systemd service unit without enabling a remote listener",
			"initialize private controller state and local audit storage",
		},
	}
	if host.OSReleaseID != "debian" {
		plan.Blockers = append(plan.Blockers, "target must run Debian")
	}
	if !host.Systemd {
		plan.Blockers = append(plan.Blockers, "systemd is unavailable")
	}
	plan.TargetSupported = len(plan.Blockers) == 0
	return plan, nil
}

func resolveStateDatabase(stateDir, configuredPath string) (string, error) {
	root, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolve state directory")
	}
	path := configuredPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve state database path")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("controller state database must stay inside the private state directory")
	}
	return path, nil
}

func countCredentialRefs(value config.Config) int {
	count := 0
	for _, adapter := range value.Adapters {
		count += len(adapter.SecretRefs)
	}
	return count
}
