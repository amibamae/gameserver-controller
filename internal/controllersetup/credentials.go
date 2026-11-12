package controllersetup

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
)

const (
	credentialScheme  = "systemd-credential://"
	maxProviderSecret = 64 << 10
)

var credentialNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Encryptor stores one credential using the host's systemd credential key.
// Implementations must read plaintext only from stdin and must not include it
// in process arguments, environment variables, or diagnostics.
type Encryptor interface {
	Encrypt(ctx context.Context, name, destination string, plaintext []byte) error
}

type SystemdEncryptor struct {
	Executable string
}

// Encrypt invokes systemd-creds with the secret on stdin and a temporary output
// path. Error output is deliberately discarded because external tools may
// include sensitive input in their diagnostics.
func (e SystemdEncryptor) Encrypt(ctx context.Context, name, destination string, plaintext []byte) error {
	executable := e.Executable
	if executable == "" {
		executable = "systemd-creds"
	}
	cmd := exec.CommandContext(ctx, executable, "encrypt", "--name="+name, "-", destination)
	cmd.Stdin = bytes.NewReader(plaintext)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("systemd credential encryption failed")
	}
	return nil
}

// ConfiguredCredentialNames returns sorted names referenced by validated
// adapter profiles without returning the references or credential values.
func ConfiguredCredentialNames(value config.Config) []string {
	seen := make(map[string]struct{})
	for _, adapter := range value.Adapters {
		for _, secret := range adapter.SecretRefs {
			name, ok := credentialName(secret.SecretRef)
			if ok {
				seen[name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SetEncryptedCredential atomically creates or replaces a configured named
// credential. The directory and output file stay private; plaintext is zeroed
// before return. Existing values are preserved unless replace is explicitly
// true. The caller must separately obtain and confirm plaintext securely.
func SetEncryptedCredential(ctx context.Context, value config.Config, name, directory string, plaintext []byte, replace bool, encryptor Encryptor) (err error) {
	defer zeroBytes(plaintext)
	if err := config.Validate(value); err != nil {
		return errors.New("controller configuration is invalid")
	}
	if encryptor == nil || !credentialNamePattern.MatchString(name) || !isConfiguredCredential(value, name) {
		return errors.New("credential name is not referenced by the validated configuration")
	}
	if !validSingleLineSecret(plaintext) {
		return errors.New("credential input is empty or invalid")
	}
	return storeEncryptedCredential(ctx, name, directory, plaintext, replace, encryptor)
}

// PilotCredentialNames are the credentials gsc-pilot reads that the
// controller configuration does not reference.
var PilotCredentialNames = []string{
	"vultr-pilot-api-token",
	"gsc-pilot-vm-wasabi-access-key-id",
	"gsc-pilot-vm-wasabi-secret-access-key",
}

// PilotSSHKeyName is the credential holding the controller's SSH private key.
const PilotSSHKeyName = "gsc-pilot-ssh-key"

// SetPilotCredential stores one single-line pilot credential, with the same
// file handling as SetEncryptedCredential.
func SetPilotCredential(ctx context.Context, name, directory string, plaintext []byte, replace bool, encryptor Encryptor) error {
	defer zeroBytes(plaintext)
	if encryptor == nil || !slices.Contains(PilotCredentialNames, name) {
		return errors.New("credential name is not a pilot credential")
	}
	if !validSingleLineSecret(plaintext) {
		return errors.New("credential input is empty or invalid")
	}
	return storeEncryptedCredential(ctx, name, directory, plaintext, replace, encryptor)
}

// SetSSHPrivateKey stores the controller's SSH private key. It accepts only
// an unencrypted OpenSSH ed25519 key, re-encoded in canonical form.
func SetSSHPrivateKey(ctx context.Context, name, directory string, key ed25519.PrivateKey, replace bool, encryptor Encryptor) error {
	if encryptor == nil || name != PilotSSHKeyName {
		return errors.New("credential name is not the pilot SSH key")
	}
	encoded, err := sshkey.MarshalPrivateKey(key, "gameserver-controller")
	if err != nil {
		return errors.New("SSH private key is invalid")
	}
	defer zeroBytes(encoded)
	return storeEncryptedCredential(ctx, name, directory, encoded, replace, encryptor)
}

func validSingleLineSecret(plaintext []byte) bool {
	if len(plaintext) == 0 || len(plaintext) > maxProviderSecret || len(bytes.TrimSpace(plaintext)) == 0 {
		return false
	}
	for _, character := range plaintext {
		if character == 0 || character == '\r' || character == '\n' {
			return false
		}
	}
	return true
}

// storeEncryptedCredential atomically creates or replaces one encrypted
// credential file. Callers validate the name and plaintext first.
func storeEncryptedCredential(ctx context.Context, name, directory string, plaintext []byte, replace bool, encryptor Encryptor) (err error) {
	if !credentialNamePattern.MatchString(name) || len(plaintext) == 0 || len(plaintext) > maxProviderSecret {
		return errors.New("credential input is empty or invalid")
	}
	if !filepath.IsAbs(directory) || strings.ContainsRune(directory, '\x00') {
		return errors.New("credential directory must be an absolute path")
	}
	if err := ensureCredentialDirectory(directory); err != nil {
		return err
	}
	finalPath := filepath.Join(directory, name)
	if info, statErr := os.Lstat(finalPath); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("existing credential target is not a regular file")
		}
		if !replace {
			return errors.New("credential already exists; explicit replacement is required")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("inspect credential target failed")
	}
	temporary, err := os.CreateTemp(directory, ".controller-credential-*")
	if err != nil {
		return errors.New("prepare encrypted credential failed")
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("prepare encrypted credential failed")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("prepare encrypted credential failed")
	}
	if err := os.Remove(temporaryPath); err != nil {
		return errors.New("prepare encrypted credential failed")
	}
	if err := encryptor.Encrypt(ctx, name, temporaryPath, plaintext); err != nil {
		return errors.New("encrypt provider credential failed")
	}
	info, err := os.Lstat(temporaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("encrypted credential output is missing or invalid")
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return errors.New("restrict encrypted credential permissions failed")
	}
	if err := syncFile(temporaryPath); err != nil {
		return errors.New("flush encrypted credential failed")
	}
	if replace {
		if err := os.Rename(temporaryPath, finalPath); err != nil {
			return errors.New("replace encrypted credential failed")
		}
	} else {
		if err := os.Link(temporaryPath, finalPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return errors.New("credential appeared during setup; no replacement was made")
			}
			return errors.New("install encrypted credential failed")
		}
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return errors.New("credential was installed but directory durability could not be confirmed")
	}
	if err := directoryFile.Sync(); err != nil {
		_ = directoryFile.Close()
		return errors.New("credential was installed but directory durability could not be confirmed")
	}
	if err := directoryFile.Close(); err != nil {
		return errors.New("credential was installed but directory durability could not be confirmed")
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func ensureCredentialDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return errors.New("create encrypted credential directory failed")
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential directory is unavailable")
	}
	if err := os.Chown(directory, os.Geteuid(), os.Getegid()); err != nil {
		return errors.New("set encrypted credential directory owner failed")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return errors.New("restrict encrypted credential directory permissions failed")
	}
	return nil
}

func isConfiguredCredential(value config.Config, name string) bool {
	for _, configured := range ConfiguredCredentialNames(value) {
		if configured == name {
			return true
		}
	}
	return false
}

func credentialName(reference string) (string, bool) {
	if !strings.HasPrefix(reference, credentialScheme) {
		return "", false
	}
	name := strings.TrimPrefix(reference, credentialScheme)
	return name, credentialNamePattern.MatchString(name)
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
