// Command controller-setup previews installation and manages configured
// encrypted systemd credentials on an existing Debian host. Service setup is
// not implemented yet.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "credentials" {
		host := inspectHost()
		if host.OSReleaseID != "debian" || !host.Systemd {
			return errors.New("credential installation requires a Debian machine running systemd")
		}
		return runCredentialCommand(args[1:], stdout, stderr, os.Geteuid() == 0, askPassword, controllersetup.SystemdEncryptor{}, "/etc/credstore.encrypted")
	}
	return runWithHost(args, stdout, stderr, inspectHost())
}

type passwordPrompt func(context.Context, string) ([]byte, error)

func runCredentialCommand(args []string, stdout, stderr io.Writer, isRoot bool, prompt passwordPrompt, encryptor controllersetup.Encryptor, credentialDirectory string) error {
	if len(args) == 0 || args[0] != "set" {
		fmt.Fprintln(stderr, "usage: controller-setup credentials set --config <config.yaml> --name <configured-credential> [--replace --confirm-replace <name>]")
		return errors.New("invalid command")
	}
	flags := flag.NewFlagSet("controller-setup credentials set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "validated controller configuration")
	name := flags.String("name", "", "configured systemd credential name")
	replace := flags.Bool("replace", false, "replace an existing encrypted credential")
	confirmReplace := flags.String("confirm-replace", "", "repeat the exact credential name to confirm replacement")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*name) == "" {
		return errors.New("--config and --name are required; no secret may be supplied as a command-line argument")
	}
	if !isRoot {
		return errors.New("credential installation must run with local administrator privileges")
	}
	if *replace != (*confirmReplace == *name) {
		return errors.New("replacement requires --replace and --confirm-replace with the exact credential name")
	}
	value, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if prompt == nil || encryptor == nil {
		return errors.New("secure credential input is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	first, err := prompt(ctx, "Enter provider credential for "+*name)
	if err != nil {
		return errors.New("secure credential prompt failed")
	}
	defer zeroSecret(first)
	second, err := prompt(ctx, "Re-enter provider credential for "+*name)
	if err != nil {
		return errors.New("credential confirmation prompt failed")
	}
	defer zeroSecret(second)
	if len(first) != len(second) || subtle.ConstantTimeCompare(first, second) != 1 {
		return errors.New("credential entries did not match")
	}
	if err := controllersetup.SetEncryptedCredential(ctx, value, *name, credentialDirectory, first, *replace, encryptor); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stored encrypted credential for configured reference %q. The value was not displayed.\n", *name)
	return nil
}

func askPassword(ctx context.Context, prompt string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "systemd-ask-password", "--timeout=90s", "-n", prompt)
	output, err := cmd.Output()
	if err != nil {
		zeroSecret(output)
		return nil, errors.New("secure prompt unavailable")
	}
	return output, nil
}

func zeroSecret(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func runWithHost(args []string, stdout, stderr io.Writer, host controllersetup.Host) error {
	if len(args) != 4 || args[0] != "plan" || args[1] != "--config" || strings.TrimSpace(args[2]) == "" || args[3] != "--json" {
		fmt.Fprintln(stderr, "usage: controller-setup plan --config <config.yaml> --json")
		return errors.New("invalid command")
	}
	value, err := config.Load(args[2])
	if err != nil {
		return err
	}
	plan, err := controllersetup.BuildPlan(value, host, defaultLayout())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

func defaultLayout() controllersetup.Layout {
	return controllersetup.Layout{
		ConfigFile:    "/etc/gameserver-controller/controller.yaml",
		ServiceFile:   "/etc/systemd/system/gameserver-controller.service",
		StateDir:      "/var/lib/gameserver-controller",
		CredentialDir: "/etc/credstore.encrypted",
	}
}

func inspectHost() controllersetup.Host {
	host := controllersetup.Host{}
	if runtime.GOOS != "linux" {
		host.OSReleaseID = runtime.GOOS
		return host
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && key == "ID" {
				host.OSReleaseID = strings.Trim(value, "\"'")
				break
			}
		}
	}
	_, err := os.Stat("/run/systemd/system")
	host.Systemd = err == nil
	return host
}
