package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// operationStatus inspects an offline store using the caller's file permissions.
func operationStatus(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("controller operation-status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("store", "", "path to the offline controller state database")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*path) == "" || flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" {
		return errors.New("operation-status requires --store PATH and one operation ID")
	}
	db, err := store.OpenBoltReadOnly(*path)
	if err != nil {
		return err
	}
	op, readErr := db.GetOperation(flags.Arg(0))
	if err := errors.Join(readErr, db.Close()); err != nil {
		return err
	}
	r := op.Record
	return json.NewEncoder(stdout).Encode(struct {
		OperationID   string    `json:"operation_id"`
		OperationType string    `json:"operation_type"`
		ServerID      string    `json:"server_id"`
		EnvironmentID string    `json:"environment_id"`
		Status        string    `json:"status"`
		CurrentStepID *string   `json:"current_step_id,omitempty"`
		UpdatedAt     time.Time `json:"updated_at"`
	}{r.OperationID, r.OperationType, r.Target.ServerID, r.Target.EnvironmentID, string(r.Status), r.CurrentStepID, r.UpdatedAt})
}
