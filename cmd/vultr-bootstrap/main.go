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

const defaultCredential = credential.SystemdReferenceScheme + "vultr-bootstrap-api-token"

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
	voicePort := flags.Uint("voice-chat-port", 0, "voice chat port")
	wasabiPath := flags.String("wasabi-remote-path", "", "Wasabi bucket or bucket/prefix used by the bootstrap")
	credentialRef := flags.String("credential-ref", defaultCredential, "systemd credential reference for the Vultr API token")
	timeout := flags.Duration("timeout", 30*time.Second, "maximum time for the Vultr API call")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if err := allowFlags(flags, "execute", "confirm-upload", "name", "controller-ip", "minecraft-port", "voice-chat-port", "wasabi-remote-path", "credential-ref", "timeout"); err != nil {
		return err
	}
	if !safeScriptName.MatchString(*name) {
		return errors.New("--name must be a lowercase Vultr startup-script name using letters, digits, and hyphens")
	}
	if *minecraftPort > 65535 || *voicePort > 65535 {
		return errors.New("ports must be between 1 and 65535")
	}
	settings := bootstrap.Settings{
		ControllerIP: *controllerIP, MinecraftPort: uint16(*minecraftPort),
		VoiceChatPort: uint16(*voicePort), WasabiRemotePath: *wasabiPath,
	}
	script, err := bootstrap.Render(settings)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(script)
	digestText := hex.EncodeToString(digest[:])
	if *timeout <= 0 || *timeout > 2*time.Minute {
		return errors.New("--timeout must be greater than zero and no more than 2 minutes")
	}
	if !*execute {
		if *confirm != "" {
			return errors.New("--confirm-upload requires --execute")
		}
		fmt.Fprintf(stdout, "Dry run: no credential was read and no Vultr API call was made.\nScript name: %s\nSHA-256: %s\nRendered bytes: %d\n", *name, digestText, len(script))
		fmt.Fprintf(stdout, "To create this startup script in Vultr, add --execute --confirm-upload %s.\n", *name)
		return nil
	}
	if *confirm != *name {
		return fmt.Errorf("upload not started: --confirm-upload must exactly match %q", *name)
	}
	if !strings.HasPrefix(*credentialRef, credential.SystemdReferenceScheme) {
		return errors.New("--credential-ref must use systemd-credential://NAME")
	}
	resolver, err := credential.FromEnvironment()
	if err != nil {
		return fmt.Errorf("load systemd credential directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var uploaded vultrapi.StartupScript
	err = resolver.WithCredential(*credentialRef, func(token []byte) error {
		var createErr error
		uploaded, createErr = client.CreateStartupScript(ctx, token, vultrapi.StartupScriptRequest{
			Name: *name, Type: "boot", Script: script,
		})
		return createErr
	})
	if err != nil {
		return fmt.Errorf("Vultr startup-script create failed; inspect the provider account before retrying if the outcome is unknown: %w", err)
	}
	fmt.Fprintf(stdout, "Created Vultr startup script.\nScript ID: %s\nName: %s\nSHA-256: %s\n", uploaded.ID, uploaded.Name, digestText)
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
