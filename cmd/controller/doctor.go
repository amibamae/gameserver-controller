package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"runtime"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/config"
)

type doctorResult struct {
	Category string `json:"category"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

func runDoctor(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("controller doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaultPath := strings.TrimSpace(os.Getenv("CONFIG_PATH"))
	if defaultPath == "" {
		defaultPath = "configs/controller.example.yaml"
	}
	path := flags.String("config", defaultPath, "controller YAML to validate")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*path) == "" {
		return errors.New("doctor requires a nonempty configuration path and accepts no positional arguments")
	}
	return reportDoctor(stdout, []doctorResult{
		checkDoctorConfig(*path),
		checkDoctorCredentials(os.Getenv("CREDENTIALS_DIRECTORY"), os.Stat),
	})
}

func reportDoctor(stdout io.Writer, results []doctorResult) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(results); err != nil {
		return fmt.Errorf("write doctor results: %w", err)
	}
	for _, result := range results {
		if result.Code == "error" {
			return errors.New("doctor found errors")
		}
	}
	return nil
}

func checkDoctorConfig(path string) doctorResult {
	const category = "configuration"
	info, err := os.Stat(path)
	if err != nil {
		return doctorStatError(category, "Configuration file", err)
	}
	if !info.Mode().IsRegular() {
		return doctorResult{category, "error", "Configuration path is not a regular file"}
	}
	if _, err := config.Load(path); err != nil {
		// Keep parsed values and host paths out of the report.
		return doctorResult{category, "error", "Configuration could not be loaded and validated; check it with config-check"}
	}
	return doctorResult{category, "ok", "Configuration passed validation; credentials and services were not tested"}
}

func checkDoctorCredentials(path string, stat func(string) (fs.FileInfo, error)) doctorResult {
	const category = "filesystem"
	if strings.TrimSpace(path) == "" {
		return doctorResult{category, "info", "Credential directory not checked: systemd supplies CREDENTIALS_DIRECTORY to the service"}
	}
	info, err := stat(path)
	if err != nil {
		return doctorStatError(category, "Credential directory", err)
	}
	if !info.IsDir() {
		return doctorResult{category, "error", "CREDENTIALS_DIRECTORY is not a directory"}
	}
	if runtime.GOOS == "windows" {
		return doctorResult{category, "info", "Credential directory exists; Windows access controls were not checked"}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return doctorResult{category, "warning", "Credential directory has group or other permission bits; review the service configuration"}
	}
	return doctorResult{category, "ok", "Credential directory has no group or other permission bits; no credential files were read"}
}

func doctorStatError(category, subject string, err error) doctorResult {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return doctorResult{category, "error", subject + " does not exist"}
	case errors.Is(err, fs.ErrPermission):
		return doctorResult{category, "error", subject + " cannot be inspected: permission denied"}
	default:
		return doctorResult{category, "error", subject + " could not be inspected"}
	}
}
