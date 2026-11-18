// Command vultr-bootstrap renders and uploads the reviewed bootstrap asset.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
	"github.com/amibamae/gameserver-controller/scripts/bootstrap"
)

const (
	defaultCredential = credential.SystemdReferenceScheme + "vultr-bootstrap-api-token"
	timeoutMin        = 2 * time.Minute
)

var safeScriptName = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)

type scriptCreator interface {
	CreateStartupScript(context.Context, []byte, vultrapi.StartupScriptRequest) (vultrapi.StartupScript, error)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, vultrapi.NewClient(nil)); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run executes the Vultr bootstrap command after parsing arguments and validating inputs.
func run(args []string, stdout, stderr io.Writer, client scriptCreator) error {
	if len(args) == 0 || args[0] != "upload" {
		return usage(stderr)
	}

	flags := flag.NewFlagSet("gsc-bootstrap upload", flag.ContinueOnError)
	flags.SetOutput(stderr)

	execute := flags.Bool("execute", false, "upload the rendered script; default is a local dry run")
	confirm := flags.String("confirm-upload", "", "repeat the exact startup-script name to approve one create request")
	name := flags.String("name", "gsc-minecraft-debian", "new Vultr startup-script name")
	controllerIP := flags.String("controller-ip", "", "public IPv4 address or CIDR of the controller allowed by the rendered firewall rules")
	minecraftPort := flags.Uint("minecraft-port", 0, "Minecraft server port")
	voiceChatPort := flags.Uint("voice-chat-port", 0, "voice chat port")
	wasabiRemotePath := flags.String("wasabi-remote-path", "", "Wasabi bucket or bucket/prefix used by the bootstrap")
	vultrPlan := flags.String("plan", "", "Vultr server plan (e.g., vc2-1-0150, vc4-1-0150, vc8-1-0150)")
	credentialRef := flags.String("credential-ref", defaultCredential, "systemd credential reference for the Vultr API token")
	timeout := flags.Duration("timeout", 30*time.Second, "maximum time for the Vultr API call")

	if parseErrors := flags.Parse(args[1:]); parseErrors != nil {
		if errors.Is(parseErrors, flag.ErrHelp) {
			return nil
		}
		return parseErrors
	}

	extraArgs := flags.Args()
	if len(extraArgs) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(extraArgs, " "))
	}

	// Extract and run all validation checks
	validationErrors := validateBootstrapArguments(bool(*execute), *confirm, *name, *controllerIP, uint(*minecraftPort), uint(*voiceChatPort), *wasabiRemotePath, *vultrPlan, *credentialRef, *timeout)
	if len(validationErrors) > 0 {
		for _, e := range validationErrors {
			fmt.Fprintln(stderr, e)
		}
		return errors.New("validation failed")
	}

	settings := buildBootstrapSettings(*controllerIP, *minecraftPort, *voiceChatPort, *wasabiRemotePath, *vultrPlan)
	renderedScript, err := bootstrap.Render(settings)
	if err != nil {
		return fmt.Errorf("render bootstrap script: %w", err)
	}

	digest := sha256.Sum256(renderedScript)
	digestText := hex.EncodeToString(digest[:])

	if !shouldExecute(*execute, *confirm, *name) {
		return handleDryRun(stdout, digestText, len(renderedScript), *name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return resolveCredentialAndUpload(ctx, stderr, stdout, digestText, *name, renderedScript, *credentialRef, client)
}

// shouldExecute returns true if the upload should be executed (dry run otherwise).
func shouldExecute(execute bool, confirm, name string) bool {
	if !execute {
		return false
	}
	if confirm == "" {
		return false
	}
	return confirm == name
}

// validateBootstrapArguments performs all validation checks and returns error messages.
func validateBootstrapArguments(execute, confirm, name, controllerIP, wasabiRemotePath, vultrPlan, credentialRef string, minecraftPort, voiceChatPort uint, timeout time.Duration) []error {
	var errs []error

	// Validate script name format
	if !safeScriptName.MatchString(name) {
		errs = append(errs, errors.New("--name must be a lowercase Vultr startup-script name using letters, digits, and hyphens"))
	}

	// Validate port ranges
	if minecraftPort > 65535 || voiceChatPort > 65535 {
		errs = append(errs, errors.New("ports must be between 1 and 65535"))
	}

	// Validate timeout range
	if timeout <= 0 || timeout > timeoutMin {
		errs = append(errs, errors.New("--timeout must be greater than zero and no more than 2 minutes"))
	}

	// Validate confirm-upload requires --execute
	if !execute && confirm != "" {
		errs = append(errs, errors.New("--confirm-upload requires --execute"))
	}

	// Validate --confirm-upload must match the provided name
	if execute && confirm != "" && confirm != name {
		errs = append(errs, fmt.Errorf("upload not started: --confirm-upload must exactly match %q", name))
	}

	// Validate credential reference format
	if !strings.HasPrefix(credentialRef, "systemd-credential://") {
		errs = append(errs, errors.New("--credential-ref must use systemd-credential://NAME"))
	}

	return errs
}

// buildBootstrapSettings creates the bootstrap.Settings from parsed flags.
func buildBootstrapSettings(controllerIP string, minecraftPort, voiceChatPort uint, wasabiRemotePath, vultrPlan string) bootstrap.Settings {
	return bootstrap.Settings{
		ControllerIP:    controllerIP,
		MinecraftPort:   uint16(minecraftPort),
		VoiceChatPort:   uint16(voiceChatPort),
		WasabiRemotePath: wasabiRemotePath,
		Plan:            vultrPlan,
	}
}

// handleDryRun outputs dry-run information without executing the upload.
func handleDryRun(stdout io.Writer, digestText string, renderedLen int, name string) error {
	fmt.Fprintf(stdout, "Dry run: no credential was read and no Vultr API call was made.\n")
	fmt.Fprintf(stdout, "Script name: %s\n", name)
	fmt.Fprintf(stdout, "SHA-256: %s\nRendered bytes: %d\n", digestText, renderedLen)
	fmt.Fprintf(stdout, "To create this startup script in Vultr, add --execute --confirm-upload %s.\n", name)
	return nil
}

// resolveCredentialAndUpload reads the credential and calls the Vultr API to create the startup script.
func resolveCredentialAndUpload(ctx context.Context, stderr io.Writer, stdout io.Writer, digestText, name string, script []byte, credentialRef string, client scriptCreator) error {
	resolver, err := credential.FromEnvironment()
	if err != nil {
		return fmt.Errorf("load systemd credential directory: %w", err)
	}

	var uploaded vultrapi.StartupScript
	err = resolver.WithCredential(credentialRef, func(token []byte) error {
		var createErr error
		uploaded, createErr = client.CreateStartupScript(ctx, token, vultrapi.StartupScriptRequest{
			Name: name, Type: "boot", Script: script,
		})
		return createErr
	})

	if err != nil {
		return fmt.Errorf("Vultr startup-script create failed; inspect the provider account before retrying if the outcome is unknown: %w", err)
	}

	fmt.Fprintf(stdout, "Created Vultr startup script.\n")
	fmt.Fprintf(stdout, "Script ID: %s\nName: %s\nSHA-256: %s\n", uploaded.ID, uploaded.Name, digestText)
	return nil
}

func allowFlags(flags *flag.FlagSet, permitted ...string) error {
	allowed := make(map[string]bool, len(permitted))
	for _, key := range permitted {
		allowed[key] = true
	}
	var unexpected []string
	flags.Visit(func(value *flag.Flag) {
		if !allowed[value.Name] {
			unexpected = append(unexpected, value.Name)
		}
	})
	if len(unexpected) != 0 {
		return fmt.Errorf("unsupported flag(s): %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func usage(w io.Writer) error {
	fmt.Fprintln(w, "Upload a reviewed Minecraft bootstrap to Vultr")
	fmt.Fprintln(w, "  gsc-bootstrap upload --name <name> --controller-ip <ip-or-CIDR> --minecraft-port <port> --voice-chat-port <port> --wasabi-remote-path <bucket/prefix>")
	fmt.Fprintln(w, "  add --execute --confirm-upload <same-name> to create the startup script")
	return errors.New("an action is required")
}
