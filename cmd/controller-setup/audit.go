package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

// auditedCommands are the subcommands that can change the host. plan and
// the help output are read-only and are not recorded.
var auditedCommands = map[string]bool{"apply": true, "credentials": true, "ssh-key": true, "keys": true}

type auditContext struct {
	log    controllersetup.AuditLog
	isRoot bool
	uid    int
}

// runAudited records a start line before an audited command runs and its
// outcome after. If the start line cannot be written, the command does not
// run, so no host change goes unrecorded. Commands run without root are not
// recorded; they refuse to change the host and cannot write the root-owned log.
func runAudited(args []string, audit auditContext, command func() error) error {
	if len(args) == 0 || !auditedCommands[args[0]] || !audit.isRoot {
		return command()
	}
	name := auditCommandName(args)
	start := controllersetup.AuditRecord{Command: name, Event: "start", UID: audit.uid}
	if err := audit.log.Append(start); err != nil {
		return fmt.Errorf("setup audit log is unavailable, so nothing was changed: %w", err)
	}
	runErr := command()
	finish := controllersetup.AuditRecord{Command: name, Event: "finish", Outcome: "succeeded", UID: audit.uid}
	if runErr != nil {
		finish.Outcome, finish.Detail = "failed", runErr.Error()
	}
	if err := audit.log.Append(finish); err != nil {
		return errors.Join(runErr, fmt.Errorf("record setup audit outcome: %w", err))
	}
	return runErr
}

// auditCommandName names the subcommand and its action word, never flag
// values, so paths and names given on the command line stay out of the log.
func auditCommandName(args []string) string {
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		return args[0] + " " + args[1]
	}
	return args[0]
}
