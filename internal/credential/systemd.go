// Package credential reads named systemd credentials for a single service.
// It does not store credentials or resolve references from arbitrary sources.
package credential

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	SystemdReferenceScheme = "systemd-credential://"
	maxCredentialBytes     = 1 << 20
)

var (
	ErrInvalidReference      = errors.New("invalid systemd credential reference")
	ErrCredentialUnavailable = errors.New("systemd credential is unavailable")
	credentialNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// Resolver is bound to one systemd credential directory. Construct it during
// service startup from CREDENTIALS_DIRECTORY, not from request input.
type Resolver struct {
	directory string
}

// FromEnvironment creates a resolver for the credential directory supplied
// to the current service by systemd.
func FromEnvironment() (*Resolver, error) {
	return NewResolver(os.Getenv("CREDENTIALS_DIRECTORY"))
}

// NewResolver validates and binds a resolver to one existing directory. The
// directory path is not a credential and may be obtained from systemd's
// CREDENTIALS_DIRECTORY environment variable.
func NewResolver(directory string) (*Resolver, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("systemd credential directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, errors.New("systemd credential directory is unavailable")
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return nil, errors.New("systemd credential directory is unavailable")
	}
	return &Resolver{directory: absolute}, nil
}

// WithCredential reads a named credential, passes its bytes to use, then
// overwrites the buffer before returning. The callback must not retain or copy
// the bytes. Go cannot guarantee removal of compiler/runtime copies from
// memory, so this only reduces the lifetime of this buffer.
func (r *Resolver) WithCredential(reference string, use func([]byte) error) error {
	if r == nil || use == nil {
		return ErrCredentialUnavailable
	}
	name, ok := credentialName(reference)
	if !ok {
		return ErrInvalidReference
	}
	value, err := r.read(name)
	if err != nil {
		return ErrCredentialUnavailable
	}
	defer zero(value)
	return use(value)
}

func credentialName(reference string) (string, bool) {
	if !strings.HasPrefix(reference, SystemdReferenceScheme) {
		return "", false
	}
	name := strings.TrimPrefix(reference, SystemdReferenceScheme)
	return name, credentialNamePattern.MatchString(name)
}

func (r *Resolver) read(name string) ([]byte, error) {
	path := filepath.Join(r.directory, name)
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, ErrCredentialUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrCredentialUnavailable
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, ErrCredentialUnavailable
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || !os.SameFile(opened, after) {
		return nil, ErrCredentialUnavailable
	}
	if opened.Size() > maxCredentialBytes {
		return nil, ErrCredentialUnavailable
	}
	value, err := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
	if err != nil || len(value) == 0 || len(value) > maxCredentialBytes {
		zero(value)
		return nil, ErrCredentialUnavailable
	}
	return value, nil
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
