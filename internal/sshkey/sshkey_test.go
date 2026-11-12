package sshkey

import (
	"bytes"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndFormats(t *testing.T) {
	public, private, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := MarshalPrivateKey(private, "gsc-test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encoded, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n")) {
		t.Fatalf("PEM header: %q", encoded[:40])
	}
	parsed, err := ParsePrivateKey(encoded)
	if err != nil || !bytes.Equal(parsed, private) {
		t.Fatalf("ParsePrivateKey = %v", err)
	}
	line := AuthorizedKey(public)
	back, err := ParseAuthorizedKey(line + " a comment")
	if err != nil || !bytes.Equal(back, public) {
		t.Fatalf("ParseAuthorizedKey = %v", err)
	}
	if canonical, _ := CanonicalAuthorizedKey(line + " comment"); canonical != line {
		t.Fatalf("canonical = %q", canonical)
	}
	if !strings.HasPrefix(Fingerprint(public), "SHA256:") || len(Fingerprint(public)) != 50 {
		t.Fatalf("fingerprint = %q", Fingerprint(public))
	}
}

// RFC 8032 test 1 key, to pin the public key encoding.
func TestKnownPublicKeyEncoding(t *testing.T) {
	seed := []byte{0x9d, 0x61, 0xb1, 0x9d, 0xef, 0xfd, 0x5a, 0x60, 0xba, 0x84, 0x4a, 0xf4, 0x92, 0xec, 0x2c, 0xc4,
		0x44, 0x49, 0xc5, 0x69, 0x7b, 0x32, 0x69, 0x19, 0x70, 0x3b, 0xac, 0x03, 0x1c, 0xae, 0x7f, 0x60}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if got := AuthorizedKey(public); got != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea" {
		t.Fatalf("authorized key = %s", got)
	}
}

func TestRejectsBadKeys(t *testing.T) {
	_, private, _ := Generate()
	encoded, _ := MarshalPrivateKey(private, "")
	for name, input := range map[string]string{
		"rsa":            "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQ==",
		"no key":         "ssh-ed25519",
		"bad base64":     "ssh-ed25519 !!!",
		"newline":        AuthorizedKey(private.Public().(ed25519.PublicKey)) + "\nssh-ed25519 x",
		"type confusion": "ssh-ed25519 AAAAB3NzaC1yc2EAAAADAQABAAAAgQ==",
	} {
		if _, err := ParseAuthorizedKey(input); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ParsePrivateKey(append(encoded, []byte("junk")...)); err == nil {
		t.Error("trailing data accepted")
	}
	// Flip one byte of the outer public key: the halves no longer agree.
	block, _ := pem.Decode(encoded)
	offset := bytes.Index(block.Bytes, private[32:])
	if offset < 0 {
		t.Fatal("public key not found in the encoding")
	}
	block.Bytes[offset] ^= 0xff
	if _, err := ParsePrivateKey(pem.EncodeToMemory(block)); err == nil {
		t.Error("tampered key accepted")
	}
	if _, err := MarshalPrivateKey(private, "bad\ncomment"); err == nil {
		t.Error("comment with a newline accepted")
	}
}

// TestOpenSSHAgrees checks both directions against the real ssh-keygen when
// it is installed: it reads our private key, and we read its keys.
func TestOpenSSHAgrees(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	dir := t.TempDir()
	public, private, _ := Generate()
	encoded, _ := MarshalPrivateKey(private, "gsc")
	path := filepath.Join(dir, "ours")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(keygen, "-y", "-f", path).Output()
	if err != nil {
		t.Fatalf("ssh-keygen could not read our key: %v", err)
	}
	if got, _ := CanonicalAuthorizedKey(strings.TrimSpace(string(out))); got != AuthorizedKey(public) {
		t.Fatalf("ssh-keygen derived %q, want %q", out, AuthorizedKey(public))
	}
	out, err = exec.Command(keygen, "-l", "-E", "sha256", "-f", path).Output()
	if err != nil || !strings.Contains(string(out), Fingerprint(public)) {
		t.Fatalf("fingerprint %q does not contain %s (%v)", out, Fingerprint(public), err)
	}

	theirs := filepath.Join(dir, "theirs")
	if err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-C", "x", "-f", theirs).Run(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(theirs)
	key, err := ParsePrivateKey(data)
	if err != nil {
		t.Fatalf("could not read an ssh-keygen key: %v", err)
	}
	pub, _ := os.ReadFile(theirs + ".pub")
	if got, _ := CanonicalAuthorizedKey(strings.TrimSpace(string(pub))); got != AuthorizedKey(key.Public().(ed25519.PublicKey)) {
		t.Fatal("public halves differ")
	}
	locked := filepath.Join(dir, "locked")
	if err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "passphrase", "-f", locked).Run(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(locked)
	if _, err := ParsePrivateKey(data); !errors.Is(err, ErrEncryptedKey) {
		t.Fatalf("encrypted key: %v", err)
	}
	rsa := filepath.Join(dir, "rsa")
	if err := exec.Command(keygen, "-q", "-t", "rsa", "-b", "2048", "-N", "", "-f", rsa).Run(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(rsa)
	if _, err := ParsePrivateKey(data); err == nil {
		t.Fatal("RSA key accepted")
	}
}
