// Command controller-setup previews installation on an existing Debian host.
// It does not install software or read provider credentials.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

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
	return runWithHost(args, stdout, stderr, inspectHost())
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
