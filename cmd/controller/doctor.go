// Copyright 2026 amibamae. All rights reserved.
// Use of this source code is governed by an MIT-style license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// DoctorResult holds a single diagnostic result from the doctor command.
type DoctorResult struct {
	Category    string   `json:"category"`     // e.g., "configuration", "filesystem", "runtime"
	Code        string   `json:"code"`         // e.g., "ok", "warning", "error"
	Message     string   `json:"message"`      // Human-readable description
	Detail      string   `json:"detail,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// formatResult converts a DoctorResult to formatted JSON.
func formatResult(r DoctorResult) string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to format result: %v\n", err)
		return "{}"
	}
	return string(data)
}

// doctorCommand runs read-only health checks and prints results to stdout.
func doctorCommand() error {
	results := make([]DoctorResult, 0, 20)

	// Check configuration
	results = append(results, checkConfiguration()...)

	// Check filesystem permissions (if CREDENTIALS_DIRECTORY is set)
	results = append(results, checkFilesystemPermissions()...)

	// Optional integrations (read-only status checks)
	results = append(results, checkOptionalIntegrations()...)

	// Check runtime state
	results = append(results, checkRuntimeState()...)

	// Print all results
	for _, r := range results {
		fmt.Println(formatResult(r))
	}

	return nil
}

// checkConfiguration validates the configuration file location.
func checkConfiguration() []DoctorResult {
	var results []DoctorResult

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		dir, _ := os.Getwd()
		configPath = fmt.Sprintf("%s/configs/controller.example.yaml", dir)
	}

	if _, err := os.Stat(configPath); errors.Is(err, fs.ErrNotExist) {
		results = append(results, DoctorResult{
			Category:   "configuration",
			Code:       "warning",
			Message:    fmt.Sprintf("Configuration file not found at default location: %s", configPath),
			Detail:     "The controller will use configs/controller.example.yaml if no CONFIG_PATH is provided.",
			Suggestions: []string{
				"Copy configs/controller.example.yaml to your desired location and modify as needed.",
				"Run 'go run ./cmd/config-check validate <path>' to validate the file before use.",
			},
		})
	} else {
		results = append(results, DoctorResult{
			Category:   "configuration",
			Code:       "ok",
			Message:    "Configuration file found at expected location",
			Detail:     fmt.Sprintf("Using: %s", configPath),
		})
	}

	return results
}

// checkFilesystemPermissions validates credential directory permissions.
func checkFilesystemPermissions() []DoctorResult {
	var results []DoctorResult

	if credDir := os.Getenv("CREDENTIALS_DIRECTORY"); credDir != "" {
		info, err := os.Stat(credDir)
		if err == nil && !info.IsDir() {
			results = append(results, DoctorResult{
				Category:   "filesystem",
				Code:       "error",
				Message:    fmt.Sprintf("CREDENTIALS_DIRECTORY '%s' is not a directory", credDir),
				Suggestions: []string{
					"Remove or correct the path so it points to an existing directory.",
				},
			})
		} else if err == nil && (info.Mode()&0o755 != 0) {
			results = append(results, DoctorResult{
				Category:   "filesystem",
				Code:       "warning",
				Message:    fmt.Sprintf("CREDENTIALS_DIRECTORY '%s' has world-readable permissions", credDir),
				Detail:     "Credentials should only be readable by root.",
				Suggestions: []string{
					"Run 'sudo chmod 700 '" + credDir + "' to restrict access.",
				},
			})
		} else {
			results = append(results, DoctorResult{
				Category:   "filesystem",
				Code:       "ok",
				Message:    fmt.Sprintf("Credential directory exists with proper permissions: %s", credDir),
			})
		}
	} else {
		results = append(results, DoctorResult{
			Category:   "configuration",
			Code:       "warning",
			Message:    "CREDENTIALS_DIRECTORY environment variable is not set",
			Detail:     "The controller will use systemd credentials if configured in the YAML.",
			Suggestions: []string{
				"Set CREDENTIALS_DIRECTORY=/run/systemd/credentials to enable encrypted credential storage.",
			},
		})
	}

	return results
}

// checkOptionalIntegrations performs read-only checks on optional integrations.
func checkOptionalIntegrations() []DoctorResult {
	var results []DoctorResult

	// Check HOMELABLE integration (if enabled)
	if homelableEnabled := os.Getenv("HOMELABLE_ENABLED"); homelableEnabled == "true" {
		homelablePath := "/etc/homelable/config.toml"
		if _, err := os.Stat(homelablePath); errors.Is(err, fs.ErrNotExist) {
			results = append(results, DoctorResult{
				Category:   "optional_integration",
				Code:       "warning",
				Message:    "homelable integration is configured but config file not found",
				Detail:     fmt.Sprintf("Expected at: %s", homelablePath),
				Suggestions: []string{
					"Create or configure the homelable integration.",
				},
			})
		} else {
			results = append(results, DoctorResult{
				Category:   "optional_integration",
				Code:       "ok",
				Message:    "homelable integration is configured",
				Detail:     fmt.Sprintf("Config found at: %s", homelablePath),
			})
		}
	}

	// Check VICTORIALOGS integration (if enabled)
	if victorialogsEnabled := os.Getenv("VICTORIALOGS_ENABLED"); victorialogsEnabled == "true" {
		vlogPath := os.Getenv("VICTORIALOGS_PATH")
		if vlogPath != "" {
			info, err := os.Stat(vlogPath)
			if errors.Is(err, fs.ErrNotExist) || !info.IsDir() {
				results = append(results, DoctorResult{
					Category:   "optional_integration",
					Code:       "warning",
					Message:    "victorialogs integration configured but path not found",
					Detail:     fmt.Sprintf("Expected at: %s", vlogPath),
					Suggestions: []string{
						"Create the log directory or adjust VICTORIALOGS_PATH.",
					},
				})
			} else {
				results = append(results, DoctorResult{
					Category:   "optional_integration",
					Code:       "ok",
					Message:    "victorialogs integration is configured",
					Detail:     fmt.Sprintf("Log directory exists: %s", vlogPath),
				})
			}
		} else {
			results = append(results, DoctorResult{
				Category:   "optional_integration",
				Code:       "warning",
				Message:    "victorialogs integration enabled but path not specified",
				Suggestions: []string{
					"Set VICTORIALOGS_PATH=/var/log/victorialogs to specify the log directory.",
				},
			})
		}
	} else {
		results = append(results, DoctorResult{
			Category:   "optional_integration",
			Code:       "info",
			Message:    "Optional integrations are not enabled",
			Detail:     "Set HOMELABLE_ENABLED or VICTORIALOGS_ENABLED to enable these features.",
		})
	}

	return results
}

// checkRuntimeState checks Go build cache and other runtime indicators.
func checkRuntimeState() []DoctorResult {
	var results []DoctorResult

	goCache := findGoCache()
	if goCache == "" {
		results = append(results, DoctorResult{
			Category:   "runtime",
			Code:       "info",
			Message:    "Go build cache not found — builds will be slower on first use",
			Suggestions: []string{
				"The Go compiler creates a cache automatically on first build.",
				"You can also run 'go clean -cache' to clear it.",
			},
		})
	} else {
		results = append(results, DoctorResult{
			Category:   "runtime",
			Code:       "ok",
			Message:    "Go build cache is present",
			Detail:     goCache,
		})
	}

	return results
}

// findGoCache returns the path to the Go module cache if found.
func findGoCache() string {
	caches := []string{
		fmt.Sprintf("%s/pkg/mod", os.Getenv("GOPATH")),
		"go/pkg/mod",
	}
	for _, c := range caches {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	return ""
}
