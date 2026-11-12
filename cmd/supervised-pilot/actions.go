package main

import (
	"fmt"
	"strings"
)

// pilotActions lists every gsc-pilot action with its usage line, in the order
// usage prints them. The unknown-action check, its error message and usage all
// read this table; runWithSettings dispatches each name, and a test keeps the
// two in step.
var pilotActions = []struct {
	name, usage string
}{
	{"status", "gsc-pilot status --host <ssh-alias>"},
	{"backup", "gsc-pilot backup --host <ssh-alias> [--execute --confirm-host <same-alias>]"},
	{"history", "gsc-pilot history"},
	{"boot-status", "gsc-pilot boot-status --server-id <id> --environment <environment>"},
	{"provision", "gsc-pilot provision --server-id <id> --environment staging --region <id> --plan <id> --os-id <id> --ssh-key-id <id> --startup-script-id <id> --startup-script-sha256 <digest> --firewall-group-id <id> --controller-ip <ip>"},
	{"adopt", "gsc-pilot adopt --server-id <id> --environment <environment> --instance-id <id> --expected-ip <IPv4> --region <id> --plan <id> --os-id <id> --os-name <name>"},
	{"handoff", "gsc-pilot handoff --operation-id <id> --wasabi-region <region> [--execute --confirm-handoff <server>@<environment>/<instance-id>]"},
	{"register-ssh-key", "gsc-pilot register-ssh-key --public-key-file <path> --name <name> [--execute]"},
	{"capacity", "gsc-pilot capacity"},
}

func knownAction(name string) bool {
	for _, action := range pilotActions {
		if action.name == name {
			return true
		}
	}
	return false
}

// unknownActionError names every action, as "a, b, or c".
func unknownActionError(name string) error {
	names := make([]string, len(pilotActions))
	for i, action := range pilotActions {
		names[i] = action.name
	}
	list := strings.Join(names[:len(names)-1], ", ") + ", or " + names[len(names)-1]
	return fmt.Errorf("unknown action %q; expected %s", name, list)
}
