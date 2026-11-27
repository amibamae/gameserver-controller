// Package controllersetup audit logs record controller installation operations.
package controllersetup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	auditDirName     = "audit"
	defaultAuditPath = "/var/log/gameserver-controller/audit.json"
)

// AuditLogger records controller setup operations. Tests inject a fake; production uses the system logger.
type AuditLogger interface {
	Log(ctx context.Context, level, component, operation string) error
}

// SystemAuditLogger is the default implementation that writes JSON logs to the audit directory.
type SystemAuditLogger struct {
	basePath string
}

func newSystemAuditLogger(basePath string) (*SystemAuditLogger, error) {
	absolute, err := filepath.Abs(basePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, err
	}
	return &SystemAuditLogger{basePath: absolute}, nil
}

// Log records an audit entry for the operation. The caller must pass context that is not canceled.
func (s *SystemAuditLogger) Log(ctx context.Context, level, component, operation string) error {
	absolute, err := filepath.Abs(s.basePath)
	if err != nil {
		return err
	}
	timestamp := time.Now().Format(time.RFC3339)
	jsonEntry := map[string]string{
		"time":      timestamp,
		"level":     level,
		"component": component,
		"operation": operation,
	}
	data, err := json.MarshalIndent(jsonEntry, "", "  ")
	if err != nil {
		return err
	}
	logFile := filepath.Join(absolute, auditDirName, operation+".log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	if err != nil {
		return err
	}
	return nil
}

// DefaultAuditPath returns the path to the audit log directory for inspection.
func DefaultAuditPath() string {
	return defaultAuditPath
}
