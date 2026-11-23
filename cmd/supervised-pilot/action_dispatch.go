package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/pilotvultr"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

// pilotCommand contains already parsed flags. Each action keeps its original
// allowlist, approval requirements and dry-run behavior.
type pilotCommand struct {
	flags     *flag.FlagSet
	settings  runtimeSettings
	stdout    io.Writer
	stderr    io.Writer
	sshKeyIDs stringListFlag
}

func (p pilotCommand) str(name string) string {
	return p.flags.Lookup(name).Value.String()
}

func (p pilotCommand) boolean(name string) bool {
	return p.str(name) == "true"
}

func (p pilotCommand) duration(name string) time.Duration {
	// FlagSet validates durations before any action is dispatched.
	value, _ := time.ParseDuration(p.str(name))
	return value
}

func (p pilotCommand) dispatch(action string) error {
	switch action {
	case "history":
		return p.runHistoryAction()
	case "status":
		return p.runStatusAction()
	case "backup":
		return p.runBackupAction()
	case "provision":
		return p.runProvisionAction()
	case "adopt":
		return p.runAdoptAction()
	case "handoff":
		return p.runHandoffAction()
	case "observability":
		return p.runObservabilityAction()
	case "register-ssh-key":
		return p.runRegisterSSHKeyAction()
	case "capacity":
		return p.runCapacityAction()
	case "boot-status":
		return p.runBootStatusAction()
	default:
		return usage(p.stderr)
	}
}

func (p pilotCommand) runHistoryAction() error {
	if p.flags.NFlag() != 0 {
		return errors.New("history is local and read-only; remove all flags")
	}
	return doHistory(p.stdout)
}

func (p pilotCommand) runStatusAction() error {
	if err := allowOnlyFlags(p.flags, "host", "execute", "confirm-host"); err != nil {
		return err
	}
	if !safeSSHHost.MatchString(p.str("host")) || strings.HasPrefix(p.str("host"), "-") {
		return errors.New("--host is required and must be a simple SSH alias or user@host")
	}
	if p.boolean("execute") || p.str("confirm-host") != "" {
		return errors.New("status is read-only; remove --execute and --confirm-host")
	}
	return doStatus(p.str("host"), p.stdout)
}

func (p pilotCommand) runBackupAction() error {
	if err := allowOnlyFlags(p.flags, "host", "execute", "confirm-host", "timeout"); err != nil {
		return err
	}
	if !safeSSHHost.MatchString(p.str("host")) || strings.HasPrefix(p.str("host"), "-") {
		return errors.New("--host is required and must be a simple SSH alias or user@host")
	}
	if p.duration("timeout") <= 0 || p.duration("timeout") > 24*time.Hour {
		return errors.New("--timeout must be greater than zero and no more than 24 hours")
	}
	return doBackup(p.str("host"), p.boolean("execute"), p.str("confirm-host"), p.duration("timeout"), p.stdout, p.stderr)
}

func (p pilotCommand) runProvisionAction() error {
	if err := allowOnlyFlags(p.flags, "execute", "confirm-create", "timeout", "server-id", "environment", "region", "plan", "os-id", "hostname", "credential-ref", "operation-id", "ssh-key-id", "startup-script-id", "startup-script-sha256", "firewall-group-id", "controller-ip", "ssh-credential-ref"); err != nil {
		return err
	}
	policy, err := pilotcapacity.LoadEmbeddedPolicy()
	if err != nil {
		return fmt.Errorf("load embedded capacity policy: %w", err)
	}
	statePath, err := p.settings.capacityStatePath()
	if err != nil {
		return fmt.Errorf("resolve capacity state path: %w", err)
	}
	return doProvision(provisionOptions{
		execute: p.boolean("execute"), confirmation: p.str("confirm-create"), timeout: p.duration("timeout"),
		policy: policy, statePath: statePath, serverID: p.str("server-id"), environment: p.str("environment"),
		region: p.str("region"), plan: p.str("plan"), osID: p.str("os-id"), hostname: p.str("hostname"),
		sshKeyIDs: p.sshKeyIDs, startupScriptID: p.str("startup-script-id"), startupScriptSHA256: p.str("startup-script-sha256"),
		credentialRef: p.str("credential-ref"), operationID: p.str("operation-id"), access: provisionAccess{FirewallGroupID: p.str("firewall-group-id"), ControllerIP: p.str("controller-ip"), SSHCredentialRef: p.str("ssh-credential-ref")},
	}, p.stdout, p.stderr)
}

func (p pilotCommand) runAdoptAction() error {
	if err := allowOnlyFlags(p.flags, "execute", "confirm-adopt", "timeout", "server-id", "environment", "instance-id", "expected-ip", "region", "plan", "os-id", "os-name", "credential-ref", "operation-id"); err != nil {
		return err
	}
	policy, err := pilotcapacity.LoadEmbeddedPolicy()
	if err != nil {
		return fmt.Errorf("load embedded capacity policy: %w", err)
	}
	statePath, err := p.settings.capacityStatePath()
	if err != nil {
		return fmt.Errorf("resolve capacity state path: %w", err)
	}
	providerOSID, err := vultrapi.ParseOSID(p.str("os-id"))
	if err != nil {
		return fmt.Errorf("parse Vultr OS ID: %w", err)
	}
	return doAdoptVultr(p.boolean("execute"), p.str("confirm-adopt"), p.duration("timeout"), policy, statePath, p.str("credential-ref"), pilotvultr.AdoptionRequest{
		OperationID: p.str("operation-id"), ServerID: p.str("server-id"), Environment: p.str("environment"), InstanceID: p.str("instance-id"),
		IP: p.str("expected-ip"), Region: p.str("region"), Plan: p.str("plan"), OSID: providerOSID, OS: p.str("os-name"),
	}, p.stdout)
}

func (p pilotCommand) runHandoffAction() error {
	if err := allowOnlyFlags(p.flags, "execute", "confirm-handoff", "timeout", "operation-id", "credential-ref", "ssh-credential-ref", "wasabi-region", "wasabi-access-key-ref", "wasabi-secret-key-ref"); err != nil {
		return err
	}
	statePath, err := p.settings.capacityStatePath()
	if err != nil {
		return fmt.Errorf("resolve capacity state path: %w", err)
	}
	handoffTimeout := p.duration("timeout")
	if !flagSet(p.flags, "timeout") {
		handoffTimeout = 45 * time.Minute
	}
	return doHandoff(handoffOptions{Execute: p.boolean("execute"), Confirmation: p.str("confirm-handoff"), Timeout: handoffTimeout, OperationID: p.str("operation-id"),
		CredentialRef: p.str("credential-ref"), SSHCredentialRef: p.str("ssh-credential-ref"), WasabiAccessRef: p.str("wasabi-access-key-ref"), WasabiSecretRef: p.str("wasabi-secret-key-ref"),
		WasabiRegion: p.str("wasabi-region")}, statePath, p.stdout, p.stderr)
}

func (p pilotCommand) runObservabilityAction() error {
	if err := allowOnlyFlags(p.flags, "execute", "confirm-observability", "operation-id", "credential-ref", "ssh-credential-ref", "logs-endpoint", "logs-privacy", "logs-credential-ref", "timeout"); err != nil {
		return err
	}
	statePath, err := p.settings.capacityStatePath()
	if err != nil {
		return fmt.Errorf("resolve capacity state path: %w", err)
	}
	return runObservability(observabilityOptions{
		Execute: p.boolean("execute"), Confirmation: p.str("confirm-observability"), OperationID: p.str("operation-id"),
		VultrCredentialRef: p.str("credential-ref"), SSHCredentialRef: p.str("ssh-credential-ref"),
		LogCredentialRef: p.str("logs-credential-ref"), Endpoint: p.str("logs-endpoint"),
		PrivacyMode: p.str("logs-privacy"), Timeout: p.duration("timeout"),
	}, statePath, p.stdout)
}

func (p pilotCommand) runRegisterSSHKeyAction() error {
	if err := allowOnlyFlags(p.flags, "execute", "public-key-file", "name", "credential-ref", "timeout"); err != nil {
		return err
	}
	return doRegisterSSHKey(p.boolean("execute"), p.str("public-key-file"), p.str("name"), p.str("credential-ref"), min(p.duration("timeout"), 2*time.Minute), p.stdout)
}

func (p pilotCommand) runCapacityAction() error {
	if p.flags.NFlag() != 0 {
		return errors.New("capacity reads the fixed local policy and state database; remove all flags")
	}
	policy, err := pilotcapacity.LoadEmbeddedPolicy()
	if err != nil {
		return fmt.Errorf("load embedded capacity policy: %w", err)
	}
	statePath, err := p.settings.capacityStatePath()
	if err != nil {
		return fmt.Errorf("resolve capacity state path: %w", err)
	}
	return doShowCapacity(policy, statePath, p.stdout)
}

func (p pilotCommand) runBootStatusAction() error {
	if err := allowOnlyFlags(p.flags, "server-id", "environment"); err != nil {
		return err
	}
	if !safePilotServerID.MatchString(p.str("server-id")) {
		return errors.New("--server-id is required and must be a lower-case identifier")
	}
	if p.str("environment") != "production" && p.str("environment") != "staging" && p.str("environment") != "testing" {
		return errors.New("--environment must be production, staging, or testing")
	}
	statePath, err := p.settings.capacityStatePath()
	if err != nil {
		return fmt.Errorf("resolve capacity state path: %w", err)
	}
	return doShowBootStatus(p.str("server-id"), p.str("environment"), statePath, time.Now().UTC(), p.stdout)
}
