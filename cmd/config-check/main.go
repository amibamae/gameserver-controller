// Command config-check validates a local controller configuration without
// resolving secrets or contacting external systems.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/controllerruntime"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) != 2 || args[0] != "validate" {
		fmt.Fprintln(stderr, "usage: config-check validate <config.yaml>")
		return fmt.Errorf("invalid command")
	}
	value, err := config.Load(args[1])
	if err != nil {
		return err
	}
	if err := controllerruntime.Validate(value); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "configuration valid (schema v%d, %d adapter profiles, %d environments, %d servers)\n",
		value.SchemaVersion, len(value.Adapters), len(value.Environments), len(value.Servers))
	return nil
}
