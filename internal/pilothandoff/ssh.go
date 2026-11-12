package pilothandoff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/sshkey"
)

// Target is one SSH destination and login.
type Target struct {
	Address string
	Port    int
	User    string
}

// Result is the outcome of one remote command.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// ErrNoHostKey means nothing answered with an ed25519 host key yet, for
// example because the VM is still booting.
var ErrNoHostKey = errors.New("the address did not present an ed25519 SSH host key")

// SSH runs OpenSSH. Every call ignores the operator's SSH configuration and
// accepts exactly one host key, given by the caller.
type SSH interface {
	// HostKey reports the ed25519 host key the address presents, without
	// logging in. It is used only to classify a connection attempt; the
	// login in Run enforces the expected key itself.
	HostKey(ctx context.Context, target Target) (string, error)
	// Run logs in and runs command, accepting only expectedHostKey. Stdin,
	// when not nil, is streamed to the remote command.
	Run(ctx context.Context, target Target, expectedHostKey, command string, stdin []byte) (Result, error)
}

// OpenSSH is the SSH implementation backed by the ssh and ssh-keyscan
// executables.
type OpenSSH struct {
	// IdentityFile is the controller's private key file.
	IdentityFile string
	// SSHPath and KeyscanPath default to "ssh" and "ssh-keyscan".
	SSHPath, KeyscanPath string
}

const (
	maxStdout = 64 << 10
	maxStderr = 8 << 10
	// hostKeyAlias is the only name in each private known_hosts file, so a
	// key pinned for one VM is never matched by address.
	hostKeyAlias = "gsc-handoff-target"
)

func (o OpenSSH) HostKey(ctx context.Context, target Target) (string, error) {
	path := o.KeyscanPath
	if path == "" {
		path = "ssh-keyscan"
	}
	cmd := exec.CommandContext(ctx, path, "-T", "10", "-t", "ed25519", "-p", strconv.Itoa(target.Port), target.Address)
	var stdout bytes.Buffer
	cmd.Stdout = &limitedWriter{buffer: &stdout, limit: maxStdout}
	cmd.Stderr = io.Discard
	_ = cmd.Run() // ssh-keyscan exits 0 even when nothing answers; judge by output
	var found []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || strings.HasPrefix(line, "#") || fields[1] != sshkey.KeyType {
			continue
		}
		key, err := sshkey.CanonicalAuthorizedKey(fields[1] + " " + fields[2])
		if err != nil {
			continue
		}
		found = append(found, key)
	}
	switch {
	case len(found) == 0:
		return "", ErrNoHostKey
	case len(found) > 1 && found[0] != found[1]:
		return "", errors.New("the address presented more than one ed25519 host key")
	}
	return found[0], nil
}

func (o OpenSSH) Run(ctx context.Context, target Target, expectedHostKey, command string, stdin []byte) (Result, error) {
	key, err := sshkey.CanonicalAuthorizedKey(expectedHostKey)
	if err != nil {
		return Result{}, errors.New("the expected host key is not a valid ed25519 key")
	}
	if !filepath.IsAbs(o.IdentityFile) {
		return Result{}, errors.New("the SSH identity file path must be absolute")
	}
	dir, err := os.MkdirTemp("", "gsc-handoff-")
	if err != nil {
		return Result{}, errors.New("could not create a private known_hosts file")
	}
	defer os.RemoveAll(dir)
	knownHosts := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(knownHosts, []byte(hostKeyAlias+" "+key+"\n"), 0o600); err != nil {
		return Result{}, errors.New("could not write a private known_hosts file")
	}
	path := o.SSHPath
	if path == "" {
		path = "ssh"
	}
	args := []string{
		"-F", "/dev/null", "-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "HostKeyAlias=" + hostKeyAlias,
		"-o", "HostKeyAlgorithms=ssh-ed25519",
		"-o", "CheckHostIP=no",
		"-o", "UpdateHostKeys=no",
		"-o", "IdentitiesOnly=yes",
		"-o", "IdentityAgent=none",
		"-o", "IdentityFile=" + o.IdentityFile,
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes",
		"-o", "LogLevel=ERROR",
		"-p", strconv.Itoa(target.Port),
		"-l", target.User,
		target.Address,
	}
	if command != "" {
		args = append(args, "--", command)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buffer: &stdout, limit: maxStdout}
	cmd.Stderr = &limitedWriter{buffer: &stderr, limit: maxStderr}
	err = cmd.Run()
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		result.ExitCode = exit.ExitCode()
	default:
		return result, fmt.Errorf("could not run ssh: %w", err)
	}
	return result, nil
}

// hostKeyRefused reports whether ssh stopped because the server's host key
// was not the expected one.
func hostKeyRefused(result Result) bool {
	text := string(result.Stderr)
	return result.ExitCode == 255 && (strings.Contains(text, "Host key verification failed") ||
		strings.Contains(text, "REMOTE HOST IDENTIFICATION HAS CHANGED") || strings.Contains(text, "No ED25519 host key is known"))
}

// authRefused reports whether the server refused the login, which before
// the bootstrap finishes means the account does not exist yet.
func authRefused(result Result) bool {
	return result.ExitCode == 255 && strings.Contains(string(result.Stderr), "Permission denied")
}

type limitedWriter struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if room := w.limit - w.buffer.Len(); room > 0 {
		if len(data) > room {
			w.buffer.Write(data[:room])
		} else {
			w.buffer.Write(data)
		}
	}
	return len(data), nil
}
