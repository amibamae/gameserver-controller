// Package controllersetup rollback procedures support atomic installation.
package controllersetup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
)

// rollbackSystemInstallerHost implements RollbackHost interface for system operations.
type rollbackSystemInstallerHost struct {
	serviceAccount string
	stateDir       string
	credentialDir  string
	configFile     string
	serviceFile    string
	binaryFile     string
}

func newRollbackSystemInstallerHost(serviceAccount, stateDir, credentialDir, configFile, serviceFile, binaryFile string) RollbackHost {
	return &rollbackSystemInstallerHost{
		serviceAccount: serviceAccount,
		stateDir:       stateDir,
		credentialDir:  credentialDir,
		configFile:     configFile,
		serviceFile:    serviceFile,
		binaryFile:     binaryFile,
	}
}

// RollbackServiceAccount removes the controller service account if it exists.
func (h *rollbackSystemInstallerHost) RollbackServiceAccount(ctx context.Context, name string) error {
	if err := exec.CommandContext(ctx, "userdel", "--remove-home", name).Run(); err != nil {
		return fmt.Errorf("remove service account %q: %w", name, err)
	}
	groupOutput, _ := exec.CommandContext(ctx, "getent", "group", name).Output()
	if len(groupOutput) == 0 {
		return errors.New("no group found for service account")
	}
	fields := strings.Fields(strings.TrimSpace(string(groupOutput)))
	if len(fields) > 0 && fields[0] == name {
		exec.CommandContext(ctx, "groupdel", name).Run()
	}
	return nil
}

// RollbackDirectory removes a directory and resets its permissions.
func (h *rollbackSystemInstallerHost) RollbackDirectory(ctx context.Context, path string, mode fs.FileMode) error {
	if err := exec.CommandContext(ctx, "rm", "-rf", path).Run(); err != nil {
		return fmt.Errorf("remove directory %q: %w", path, err)
	}
	parent := filepath.Dir(path)
	exec.CommandContext(ctx, "chmod", "--", mode.String(), parent).Run()
	return nil
}

// RollbackFile removes a file and returns the path that was deleted.
func (h *rollbackSystemInstallerHost) RollbackFile(ctx context.Context, path string, file InstallFile) error {
	exec.CommandContext(ctx, "rm", "-f", path).Run()
	return nil
}

// SystemctlStopAndDisable stops and disables a systemd unit for rollback purposes.
func (h *rollbackSystemInstallerHost) SystemctlStopAndDisable(ctx context.Context, name string) error {
	if err := exec.CommandContext(ctx, "systemctl", "stop", name).Run(); err != nil {
		return fmt.Errorf("stop %q: %w", name, err)
	}
	if err := exec.CommandContext(ctx, "systemctl", "disable", name).Run(); err != nil {
		return fmt.Errorf("disable %q: %w", name, err)
	}
	return nil
}
