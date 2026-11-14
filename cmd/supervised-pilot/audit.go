package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

var safeOperationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var safePilotTarget = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}@(production|staging|testing)$`)

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
	return appendAuditEvent(path, operationID, event, "backup", host, backupUnit, detail)
}

func appendAuditEvent(path, operationID, event, action, target, service, detail string) error {
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
		Action:      action,
		Target:      target,
		Service:     service,
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
		fmt.Fprintln(stdout, "No pilot operations have been recorded.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("open audit log %s: %w", auditPath, err)
	}
	// History reports scan errors independently of file cleanup.
	defer func() { _ = file.Close() }()
	return writeAuditHistory(stdout, file, auditPath)
}

func writeAuditHistory(stdout io.Writer, file io.Reader, auditPath string) error {

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNumber := 0
	wroteHeader := false
	for scanner.Scan() {
		lineNumber++
		record, recordedAt, err := decodeAuditRecord(scanner.Bytes(), auditPath, lineNumber)
		if err != nil {
			return err
		}
		if !wroteHeader {
			fmt.Fprintln(stdout, "RECORDED_AT\tOPERATION_ID\tACTION\tEVENT\tTARGET\tSERVICE\tDETAIL")
			wroteHeader = true
		}
		detail := "-"
		if record.Detail != "" {
			detail = strconv.Quote(record.Detail)
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			recordedAt.UTC().Format(time.RFC3339Nano), record.OperationID, record.Action,
			record.Event, record.Target, record.Service, detail)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read audit log %s: %w", auditPath, err)
	}
	if !wroteHeader {
		fmt.Fprintln(stdout, "No pilot operations have been recorded.")
	}
	return nil
}

func decodeAuditRecord(raw []byte, auditPath string, lineNumber int) (auditRecord, time.Time, error) {
	var record auditRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return auditRecord{}, time.Time{}, fmt.Errorf("decode audit log %s line %d: %w", auditPath, lineNumber, err)
	}
	recordedAt, err := time.Parse(time.RFC3339Nano, record.RecordedAt)
	if err != nil {
		return auditRecord{}, time.Time{}, fmt.Errorf("invalid timestamp in audit log %s line %d", auditPath, lineNumber)
	}
	if !validAuditRecord(record) {
		return auditRecord{}, time.Time{}, fmt.Errorf("invalid audit record in %s line %d", auditPath, lineNumber)
	}
	return record, recordedAt, nil
}

func validAuditRecord(record auditRecord) bool {
	if !safeOperationID.MatchString(record.OperationID) {
		return false
	}
	switch record.Action {
	case "backup":
		return record.Service == backupUnit && safeSSHHost.MatchString(record.Target) && validBackupAuditEvent(record.Event)
	case "adopt":
		return record.Service == "vultr.instance.adopt" && safePilotTarget.MatchString(record.Target) && validAdoptionAuditEvent(record.Event)
	case "provision":
		return record.Service == "vultr.instance.create" && safePilotTarget.MatchString(record.Target) && validProvisionAuditEvent(record.Event)
	default:
		return false
	}
}

func validBackupAuditEvent(event string) bool {
	switch event {
	case "rejected", "started", "outcome_unknown", "failed", "service_succeeded", "succeeded":
		return true
	default:
		return false
	}
}

func validProvisionAuditEvent(event string) bool {
	switch event {
	case "outcome_unknown", "provision_requested", "provision_failed", "provision_rejected", "provider_rejected", "provision_confirmed":
		return true
	default:
		return false
	}
}

func validAdoptionAuditEvent(event string) bool {
	return event == "adoption_requested" || event == "adoption_failed" || event == "adoption_confirmed"
}
