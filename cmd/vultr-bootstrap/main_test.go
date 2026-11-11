package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

type fakeCreator struct {
	calls int
	input vultrapi.StartupScriptRequest
	token string
}

func (f *fakeCreator) CreateStartupScript(_ context.Context, token []byte, input vultrapi.StartupScriptRequest) (vultrapi.StartupScript, error) {
	f.calls++
	f.input = input
	f.token = string(token)
	return vultrapi.StartupScript{ID: "synthetic-script-id", Name: input.Name}, nil
}

func TestUploadDryRunDoesNotReadCredentialOrCallProvider(t *testing.T) {
	fake := &fakeCreator{}
	var stdout, stderr strings.Builder
	err := run(validArgs(), &stdout, &stderr, fake)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 || !strings.Contains(stdout.String(), "Dry run") || !strings.Contains(stdout.String(), "SHA-256:") {
		t.Fatalf("unexpected dry-run behavior: calls=%d output=%q", fake.calls, stdout.String())
	}
}

func TestUploadRequiresExactConfirmationBeforeCredentialAccess(t *testing.T) {
	fake := &fakeCreator{}
	args := append(validArgs(), "--execute")
	err := run(args, io.Discard, io.Discard, fake)
	if err == nil || !strings.Contains(err.Error(), "confirm-upload") || fake.calls != 0 {
		t.Fatalf("upload without exact confirmation was not rejected: err=%v calls=%d", err, fake.calls)
	}
}

func TestUploadReadsSystemdCredentialAndPrintsOnlyMetadata(t *testing.T) {
	credentials := t.TempDir()
	if err := os.WriteFile(filepath.Join(credentials, "vultr-bootstrap-api-token"), []byte("synthetic-token"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", credentials)
	fake := &fakeCreator{}
	args := append(validArgs(), "--execute", "--confirm-upload", "gsc-minecraft-debian")
	var stdout strings.Builder
	if err := run(args, &stdout, io.Discard, fake); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || fake.token != "synthetic-token" || fake.input.Name != "gsc-minecraft-debian" || fake.input.Type != "boot" {
		t.Fatalf("unexpected provider request metadata: calls=%d name=%q type=%q", fake.calls, fake.input.Name, fake.input.Type)
	}
	digest := sha256.Sum256(fake.input.Script)
	if !strings.Contains(stdout.String(), "synthetic-script-id") || !strings.Contains(stdout.String(), hex.EncodeToString(digest[:])) {
		t.Fatalf("upload result omitted provider ID or rendered digest: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "synthetic-token") || strings.Contains(stdout.String(), "example-bucket") || strings.Contains(stdout.String(), "203.0.113.10") {
		t.Fatalf("upload output leaked credential or deployment setting: %q", stdout.String())
	}
}

func validArgs() []string {
	return []string{"upload", "--name", "gsc-minecraft-debian", "--controller-ip", "203.0.113.10", "--minecraft-port", "25565", "--voice-chat-port", "24454", "--wasabi-remote-path", "example-bucket/backups"}
}
