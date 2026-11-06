package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	gameUnit   = "minecraft-vanilla.service"
	backupUnit = "minecraft-backup.service"
	timerUnit  = "minecraft-backup.timer"
)

var safeSSHHost = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$`)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usage(stderr)
	}

	action := args[0]
	if action != "status" && action != "backup" {
		return fmt.Errorf("unknown action %q; expected status or backup", action)
	}

	flags := flag.NewFlagSet("gsc-pilot "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "SSH config alias or user@host for the Minecraft server")
	execute := flags.Bool("execute", false, "execute the backup action; without this flag, backup is a dry run")
	confirmHost := flags.String("confirm-host", "", "repeat the exact SSH target to approve a backup run")
	timeout := flags.Duration("timeout", time.Hour, "maximum time to wait for a backup service run")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if !safeSSHHost.MatchString(*host) {
		return errors.New("--host is required and must be a simple SSH alias or user@host")
	}
	if strings.HasPrefix(*host, "-") {
		return errors.New("--host must not start with a dash")
	}

	switch action {
	case "status":
		if *execute || *confirmHost != "" {
			return errors.New("status is read-only; remove --execute and --confirm-host")
		}
		return showStatus(*host, stdout)
	case "backup":
		if *timeout <= 0 || *timeout > 24*time.Hour {
			return errors.New("--timeout must be greater than zero and no more than 24 hours")
		}
		return backup(*host, *execute, *confirmHost, *timeout, stdout, stderr)
	default:
		return usage(stderr)
	}
}

func usage(w io.Writer) error {
	fmt.Fprintln(w, "Supervised Minecraft server pilot")
	fmt.Fprintln(w, "  gsc-pilot status --host <ssh-alias>")
	fmt.Fprintln(w, "  gsc-pilot backup --host <ssh-alias> [--execute --confirm-host <same-alias>]")
	return errors.New("an action is required")
}

func showStatus(host string, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for _, unit := range []string{gameUnit, backupUnit, timerUnit} {
		state, err := readUnitState(ctx, host, unit)
		if err != nil {
			return fmt.Errorf("read %s on %s: %w", unit, host, err)
		}
		fmt.Fprintf(stdout, "%s: active=%s sub=%s result=%s exit=%s\n",
			unit, state.active, state.sub, state.result, state.exitCode)
	}
	return nil
}

func backup(host string, execute bool, confirmation string, timeout time.Duration, stdout, stderr io.Writer) error {
	if !execute {
		fmt.Fprintf(stdout, "Dry run: would check %s on %s, start it, and verify its result.\n", backupUnit, host)
		fmt.Fprintln(stdout, "The Paper service remains online; this uses the existing backup service.")
		fmt.Fprintf(stdout, "To run it, add --execute --confirm-host %s\n", host)
		return nil
	}
	if confirmation != host {
		return errors.New("backup not started: --confirm-host must exactly match --host")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	before, err := readUnitState(ctx, host, backupUnit)
	if err != nil {
		return fmt.Errorf("preflight %s on %s: %w", backupUnit, host, err)
	}
	if before.active != "inactive" {
		return fmt.Errorf("refusing to start %s while its state is %q; inspect the unit manually first", backupUnit, before.active)
	}
	if before.result == "failed" {
		return fmt.Errorf("refusing to retry failed unit %s; inspect its journal and clear the failure manually", backupUnit)
	}

	fmt.Fprintf(stdout, "Starting %s on %s (timeout %s).\n", backupUnit, host, timeout)
	fmt.Fprintln(stdout, "No stop, restore, or destroy command will be sent.")
	if output, err := remote(ctx, host, "sudo -n systemctl start --wait "+backupUnit); err != nil {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		return fmt.Errorf("start %s: %w", backupUnit, err)
	}

	after, err := readUnitState(ctx, host, backupUnit)
	if err != nil {
		return fmt.Errorf("verify %s on %s: %w", backupUnit, host, err)
	}
	if after.result != "success" || after.exitCode != "0" {
		return fmt.Errorf("%s did not report success (result=%q, exit=%q, active=%q)", backupUnit, after.result, after.exitCode, after.active)
	}
	fmt.Fprintf(stdout, "Backup service completed successfully on %s.\n", host)
	fmt.Fprintf(stdout, "%s: active=%s sub=%s result=%s exit=%s\n",
		backupUnit, after.active, after.sub, after.result, after.exitCode)
	return nil
}

type unitState struct {
	active   string
	sub      string
	result   string
	exitCode string
}

func readUnitState(ctx context.Context, host, unit string) (unitState, error) {
	output, err := remote(ctx, host, "sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus "+unit)
	if err != nil {
		if output != "" {
			return unitState{}, fmt.Errorf("%w: %s", err, output)
		}
		return unitState{}, err
	}
	values := make(map[string]string, 4)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			values[name] = value
		}
	}
	state, hasActive := values["ActiveState"]
	sub, hasSub := values["SubState"]
	if !hasActive || !hasSub {
		return unitState{}, fmt.Errorf("unexpected systemctl response for %s", unit)
	}
	return unitState{
		active:   state,
		sub:      sub,
		result:   values["Result"],
		exitCode: values["ExecMainStatus"],
	}, nil
}

func remote(ctx context.Context, host, command string) (string, error) {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, command}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

