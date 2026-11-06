package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	gameUnit   = "minecraft-vanilla.service"
	backupUnit = "minecraft-backup.service"
	timerUnit  = "minecraft-backup.timer"
)

var safeSSHHost = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$`)
var safeOperationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var safeInvocationID = regexp.MustCompile(`^[0-9a-f]{32}$`)

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
	if action != "status" && action != "backup" && action != "history" {
		return fmt.Errorf("unknown action %q; expected status, backup, or history", action)
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
	remoteFlagProvided := false
	flags.Visit(func(value *flag.Flag) {
		switch value.Name {
		case "host", "execute", "confirm-host", "timeout":
			remoteFlagProvided = true
		}
	})
	if action == "history" {
		if remoteFlagProvided {
			return errors.New("history is local and read-only; remove server-related flags")
		}
		return showHistory(stdout)
	}
	if !safeSSHHost.MatchString(*host) || strings.HasPrefix(*host, "-") {
		return errors.New("--host is required and must be a simple SSH alias or user@host")
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
	fmt.Fprintln(w, "  gsc-pilot history")
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
		fmt.Fprintf(stdout, "%s: active=%s sub=%s result=%s exit=%s invocation=%s\n",
			unit, state.active, state.sub, state.result, state.exitCode, state.invocationID)
	}
	return nil
}

func backup(host string, execute bool, confirmation string, timeout time.Duration, stdout, stderr io.Writer) error {
	if !execute {
		fmt.Fprintf(stdout, "Dry run: would check %s on %s, start it, and inspect the resulting systemd invocation.\n", backupUnit, host)
		fmt.Fprintln(stdout, "The Paper service remains online; this uses the existing backup service.")
		fmt.Fprintf(stdout, "To run it, add --execute --confirm-host %s\n", host)
		return nil
	}
	if confirmation != host {
		return errors.New("backup not started: --confirm-host must exactly match --host")
	}

	operationID, err := newOperationID()
	if err != nil {
		return fmt.Errorf("create operation ID: %w", err)
	}
	auditPath, err := defaultAuditPath()
	if err != nil {
		return fmt.Errorf("resolve audit log path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	before, err := readUnitState(ctx, host, backupUnit)
	if err != nil {
		return rejected(host, operationID, auditPath, "preflight_failed", fmt.Errorf("preflight %s on %s: %w", backupUnit, host, err))
	}
	if before.active == "failed" || before.result == "failed" {
		return rejected(host, operationID, auditPath, "unit_failed", fmt.Errorf("refusing to retry failed unit %s; inspect its journal and clear the failure manually", backupUnit))
	}
	if before.active != "inactive" {
		return rejected(host, operationID, auditPath, "unit_not_inactive", fmt.Errorf("refusing to start %s while its state is %q; inspect the unit manually first", backupUnit, before.active))
	}
	if err := appendAuditRecord(auditPath, operationID, "started", host, fmt.Sprintf("previous systemd invocation_id=%q", before.invocationID)); err != nil {
		return fmt.Errorf("backup not started (operation %s): could not persist the audit record at %s: %w", operationID, auditPath, err)
	}

	fmt.Fprintf(stdout, "Starting %s on %s (timeout %s).\n", backupUnit, host, timeout)
	fmt.Fprintf(stdout, "Operation ID: %s\nAudit log: %s\n", operationID, auditPath)
	fmt.Fprintln(stdout, "No stop, restore, or destroy command will be sent.")
	if output, err := remote(ctx, host, "sudo -n systemctl start --wait "+backupUnit); err != nil {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		detail := fmt.Sprintf("systemctl start returned an error; remote outcome may be unknown (previous invocation_id=%q)", before.invocationID)
		if auditErr := appendAuditRecord(auditPath, operationID, "outcome_unknown", host, detail); auditErr != nil {
			return fmt.Errorf("backup outcome unknown (%v); audit update also failed: %w", err, auditErr)
		}
		return fmt.Errorf("backup outcome unknown; inspect %s on %s: %w", backupUnit, host, err)
	}

	after, err := readUnitState(ctx, host, backupUnit)
	if err != nil {
		detail := "start returned successfully, but post-run status could not be read"
		if auditErr := appendAuditRecord(auditPath, operationID, "outcome_unknown", host, detail); auditErr != nil {
			return fmt.Errorf("%s on %s: %v; audit update also failed: %w", backupUnit, host, err, auditErr)
		}
		return fmt.Errorf("backup outcome unknown; verify %s on %s: %w", backupUnit, host, err)
	}
	if !safeInvocationID.MatchString(after.invocationID) ||
		(before.invocationID != "" && (!safeInvocationID.MatchString(before.invocationID) || before.invocationID == after.invocationID)) {
		detail := fmt.Sprintf("systemd invocation ID is missing, malformed, or unchanged: before=%q after=%q", before.invocationID, after.invocationID)
		if auditErr := appendAuditRecord(auditPath, operationID, "outcome_unknown", host, detail); auditErr != nil {
			return fmt.Errorf("backup outcome unknown (%s); audit update also failed: %w", detail, auditErr)
		}
		return fmt.Errorf("backup outcome unknown; %s; inspect %s on %s before retrying", detail, backupUnit, host)
	}
	if after.result != "success" || after.exitCode != "0" {
		detail := fmt.Sprintf("systemd invocation_id=%q result=%q exit=%q active=%q", after.invocationID, after.result, after.exitCode, after.active)
		if auditErr := appendAuditRecord(auditPath, operationID, "failed", host, detail); auditErr != nil {
			return fmt.Errorf("%s did not report success (%s); audit update failed: %w", backupUnit, detail, auditErr)
		}
		return fmt.Errorf("%s did not report success (%s)", backupUnit, detail)
	}
	if err := appendAuditRecord(auditPath, operationID, "service_succeeded", host, fmt.Sprintf("systemd invocation_id=%s reported result=success and exit status 0; backup contents and storage integrity are not verified", after.invocationID)); err != nil {
		return fmt.Errorf("%s reported success, but the result could not be recorded in %s: %w", backupUnit, auditPath, err)
	}
	fmt.Fprintf(stdout, "Backup service completed successfully on %s (operation %s).\n", host, operationID)
	fmt.Fprintln(stdout, "This confirms the systemd unit result only; it does not verify backup contents or storage integrity.")
	fmt.Fprintf(stdout, "%s: active=%s sub=%s result=%s exit=%s invocation=%s\n",
		backupUnit, after.active, after.sub, after.result, after.exitCode, after.invocationID)
	return nil
}

func rejected(host, operationID, auditPath, reason string, cause error) error {
	if err := appendAuditRecord(auditPath, operationID, "rejected", host, reason); err != nil {
		return fmt.Errorf("operation %s rejected: %w; could not record rejection in %s: %v", operationID, cause, auditPath, err)
	}
	return fmt.Errorf("operation %s rejected (audit log: %s): %w", operationID, auditPath, cause)
}

type auditRecord struct {
	OperationID string `json:"operation_id"`
	RecordedAt  string `json:"recorded_at"`
	Event       string `json:"event"`
	Action      string `json:"action"`
	Target      string `json:"target"`
	Service     string `json:"service"`
	Detail      string `json:"detail,omitempty"`
}

func newOperationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func defaultAuditPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl"), nil
}

func appendAuditRecord(path, operationID, event, host, detail string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	record := auditRecord{
		OperationID: operationID,
		RecordedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Event:       event,
		Action:      "backup",
		Target:      host,
		Service:     backupUnit,
		Detail:      detail,
	}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func showHistory(stdout io.Writer) error {
	auditPath, err := defaultAuditPath()
	if err != nil {
		return fmt.Errorf("resolve audit log path: %w", err)
	}
	file, err := os.Open(auditPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "No backup operations have been recorded.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("open audit log %s: %w", auditPath, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNumber := 0
	wroteHeader := false
	for scanner.Scan() {
		lineNumber++
		var record auditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("decode audit log %s line %d: %w", auditPath, lineNumber, err)
		}
		recordedAt, err := time.Parse(time.RFC3339Nano, record.RecordedAt)
		if err != nil {
			return fmt.Errorf("invalid timestamp in audit log %s line %d", auditPath, lineNumber)
		}
		if !safeOperationID.MatchString(record.OperationID) ||
			record.Action != "backup" || record.Service != backupUnit ||
			!safeSSHHost.MatchString(record.Target) || !validAuditEvent(record.Event) {
			return fmt.Errorf("invalid audit record in %s line %d", auditPath, lineNumber)
		}
		if !wroteHeader {
			fmt.Fprintln(stdout, "RECORDED_AT\tOPERATION_ID\tEVENT\tTARGET\tSERVICE\tDETAIL")
			wroteHeader = true
		}
		detail := "-"
		if record.Detail != "" {
			detail = strconv.Quote(record.Detail)
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\n",
			recordedAt.UTC().Format(time.RFC3339Nano), record.OperationID,
			record.Event, record.Target, record.Service, detail)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read audit log %s: %w", auditPath, err)
	}
	if !wroteHeader {
		fmt.Fprintln(stdout, "No backup operations have been recorded.")
	}
	return nil
}

func validAuditEvent(event string) bool {
	switch event {
	case "rejected", "started", "outcome_unknown", "failed", "service_succeeded", "succeeded":
		return true
	default:
		return false
	}
}

type unitState struct {
	active       string
	sub          string
	result       string
	exitCode     string
	invocationID string
}

func readUnitState(ctx context.Context, host, unit string) (unitState, error) {
	output, err := remote(ctx, host, "sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus,InvocationID "+unit)
	if err != nil {
		if output != "" {
			return unitState{}, fmt.Errorf("%w: %s", err, output)
		}
		return unitState{}, err
	}
	values := make(map[string]string, 5)
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
		active:       state,
		sub:          sub,
		result:       values["Result"],
		exitCode:     values["ExecMainStatus"],
		invocationID: values["InvocationID"],
	}, nil
}

func remote(ctx context.Context, host, command string) (string, error) {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, command}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
