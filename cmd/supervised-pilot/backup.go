package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// backup starts the existing backup unit once and checks the systemd
// invocation it produced. Without --execute it is a dry run that makes no SSH
// call; with it, --confirm-host must repeat --host exactly.
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
	run, err := newBackupRun(host, stdout, stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return run.execute(ctx, timeout)
}

// backupRun is one confirmed backup operation and its audit trail.
type backupRun struct {
	host        string
	operationID string
	auditPath   string
	stdout      io.Writer
	stderr      io.Writer
}

func newBackupRun(host string, stdout, stderr io.Writer) (backupRun, error) {
	operationID, err := newOperationID()
	if err != nil {
		return backupRun{}, fmt.Errorf("create operation ID: %w", err)
	}
	auditPath, err := defaultAuditPath()
	if err != nil {
		return backupRun{}, fmt.Errorf("resolve audit log path: %w", err)
	}
	return backupRun{host: host, operationID: operationID, auditPath: auditPath, stdout: stdout, stderr: stderr}, nil
}

func (run backupRun) audit(event, detail string) error {
	return appendAuditRecord(run.auditPath, run.operationID, event, run.host, detail)
}

func (run backupRun) execute(ctx context.Context, timeout time.Duration) error {
	before, err := run.preflight(ctx)
	if err != nil {
		return err
	}
	if err := run.audit("started", fmt.Sprintf("previous systemd invocation_id=%q", before.invocationID)); err != nil {
		return fmt.Errorf("backup not started (operation %s): could not persist the audit record at %s: %w", run.operationID, run.auditPath, err)
	}
	fmt.Fprintf(run.stdout, "Starting %s on %s (timeout %s).\n", backupUnit, run.host, timeout)
	fmt.Fprintf(run.stdout, "Operation ID: %s\nAudit log: %s\n", run.operationID, run.auditPath)
	fmt.Fprintln(run.stdout, "No stop, restore, or destroy command will be sent.")
	if err := run.start(ctx, before); err != nil {
		return err
	}
	after, err := run.inspect(ctx, before)
	if err != nil {
		return err
	}
	return run.report(after)
}

// preflight reads the unit before the start and refuses, with an audited
// rejection, a unit that failed or is not idle.
func (run backupRun) preflight(ctx context.Context) (unitState, error) {
	before, err := readUnitState(ctx, run.host, backupUnit)
	if err != nil {
		return unitState{}, rejected(run.host, run.operationID, run.auditPath, "preflight_failed", fmt.Errorf("preflight %s on %s: %w", backupUnit, run.host, err))
	}
	if reason, cause := preflightRefusal(before); cause != nil {
		return unitState{}, rejected(run.host, run.operationID, run.auditPath, reason, cause)
	}
	return before, nil
}

// preflightRefusal names why a unit in this state must not be started, or
// returns a nil error when it may be.
func preflightRefusal(before unitState) (string, error) {
	if before.active == "failed" || before.result == "failed" {
		return "unit_failed", fmt.Errorf("refusing to retry failed unit %s; inspect its journal and clear the failure manually", backupUnit)
	}
	if before.active != "inactive" {
		return "unit_not_inactive", fmt.Errorf("refusing to start %s while its state is %q; inspect the unit manually first", backupUnit, before.active)
	}
	return "", nil
}

// start runs the unit and waits for it. A failed start leaves the remote
// outcome unknown; it is audited and never retried.
func (run backupRun) start(ctx context.Context, before unitState) error {
	output, err := remote(ctx, run.host, "sudo -n systemctl start --wait "+backupUnit)
	if err == nil {
		return nil
	}
	if output != "" {
		fmt.Fprintln(run.stderr, output)
	}
	detail := fmt.Sprintf("systemctl start returned an error; remote outcome may be unknown (previous invocation_id=%q)", before.invocationID)
	if auditErr := run.audit("outcome_unknown", detail); auditErr != nil {
		return fmt.Errorf("backup outcome unknown (%v); audit update also failed: %w", err, auditErr)
	}
	return fmt.Errorf("backup outcome unknown; inspect %s on %s: %w", backupUnit, run.host, err)
}

// inspect reads the unit after the start and requires a new, finished
// systemd invocation.
func (run backupRun) inspect(ctx context.Context, before unitState) (unitState, error) {
	after, err := readUnitState(ctx, run.host, backupUnit)
	if err != nil {
		detail := "start returned successfully, but post-run status could not be read"
		if auditErr := run.audit("outcome_unknown", detail); auditErr != nil {
			return unitState{}, fmt.Errorf("%s on %s: %v; audit update also failed: %w", backupUnit, run.host, err, auditErr)
		}
		return unitState{}, fmt.Errorf("backup outcome unknown; verify %s on %s: %w", backupUnit, run.host, err)
	}
	if detail := unconfirmedInvocation(before, after); detail != "" {
		return unitState{}, run.outcomeUnknown(detail)
	}
	return after, nil
}

// unconfirmedInvocation describes why after does not prove that this start
// ran to completion, or returns "" when it does.
func unconfirmedInvocation(before, after unitState) string {
	if !invocationReplaced(before, after) {
		return fmt.Sprintf("systemd invocation ID is missing, malformed, or unchanged: before=%q after=%q", before.invocationID, after.invocationID)
	}
	if !systemdInvocationFinished(after) {
		return fmt.Sprintf("systemd start job returned before the unit reached a completed state: invocation_id=%q active=%q sub=%q", after.invocationID, after.active, after.sub)
	}
	return ""
}

// invocationReplaced reports whether after carries a well-formed invocation
// ID that differs from a well-formed (or absent) previous one.
func invocationReplaced(before, after unitState) bool {
	if !safeInvocationID.MatchString(after.invocationID) {
		return false
	}
	return before.invocationID == "" || (safeInvocationID.MatchString(before.invocationID) && before.invocationID != after.invocationID)
}

func (run backupRun) outcomeUnknown(detail string) error {
	if auditErr := run.audit("outcome_unknown", detail); auditErr != nil {
		return fmt.Errorf("backup outcome unknown (%s); audit update also failed: %w", detail, auditErr)
	}
	return fmt.Errorf("backup outcome unknown; %s; inspect %s on %s before retrying", detail, backupUnit, run.host)
}

// backupSucceeded reports whether the finished invocation exited cleanly.
func backupSucceeded(after unitState) bool {
	return after.active != "failed" && after.result == "success" && after.exitCode == "0"
}

func (run backupRun) report(after unitState) error {
	if !backupSucceeded(after) {
		detail := fmt.Sprintf("systemd invocation_id=%q result=%q exit=%q active=%q", after.invocationID, after.result, after.exitCode, after.active)
		if auditErr := run.audit("failed", detail); auditErr != nil {
			return fmt.Errorf("%s did not report success (%s); audit update failed: %w", backupUnit, detail, auditErr)
		}
		return fmt.Errorf("%s did not report success (%s)", backupUnit, detail)
	}
	if err := run.audit("service_succeeded", fmt.Sprintf("systemd invocation_id=%s reported result=success and exit status 0; backup contents and storage integrity are not verified", after.invocationID)); err != nil {
		return fmt.Errorf("%s reported success, but the result could not be recorded in %s: %w", backupUnit, run.auditPath, err)
	}
	fmt.Fprintf(run.stdout, "Backup service completed successfully on %s (operation %s).\n", run.host, run.operationID)
	fmt.Fprintln(run.stdout, "This confirms the systemd unit result only; it does not verify backup contents or storage integrity.")
	fmt.Fprintf(run.stdout, "%s: active=%s sub=%s result=%s exit=%s invocation=%s\n",
		backupUnit, after.active, after.sub, after.result, after.exitCode, after.invocationID)
	return nil
}
