// Package sshkey generates, encodes, and parses ed25519 SSH keys using only
// the standard library. It writes the OpenSSH private key format that ssh,
// ssh-keygen, and cloud-init read, and the authorized_keys public key form.
// Only unencrypted ed25519 keys are supported.
package sshkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"strings"
)

// KeyType is the only SSH key type this package handles.
const KeyType = "ssh-ed25519"

const (
	privateKeyMagic = "openssh-key-v1\x00"
	pemType         = "OPENSSH PRIVATE KEY"
	maxCommentBytes = 256
)

var (
	ErrInvalidKey     = errors.New("not a valid unencrypted ed25519 OpenSSH key")
	ErrEncryptedKey   = errors.New("the private key is passphrase-protected; supply an unencrypted key")
	ErrUnsupportedKey = errors.New("only ed25519 keys are supported")
	errTruncated      = errors.New("truncated key data")
)

// Generate returns a new ed25519 key pair from crypto/rand.
func Generate() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// wireKey is the SSH wire encoding of an ed25519 public key.
func wireKey(public ed25519.PublicKey) []byte {
	var b bytes.Buffer
	writeString(&b, []byte(KeyType))
	writeString(&b, public)
	return b.Bytes()
}

// AuthorizedKey renders "ssh-ed25519 <base64>" with no comment.
func AuthorizedKey(public ed25519.PublicKey) string {
	return KeyType + " " + base64.StdEncoding.EncodeToString(wireKey(public))
}

// Fingerprint renders the OpenSSH SHA256 fingerprint ("SHA256:...").
func Fingerprint(public ed25519.PublicKey) string {
	sum := sha256.Sum256(wireKey(public))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// ParseAuthorizedKey accepts exactly "ssh-ed25519 <base64>", optionally
// followed by one space and a comment, and returns the key. The base64 must
// be canonical and decode to exactly one ed25519 wire key.
func ParseAuthorizedKey(line string) (ed25519.PublicKey, error) {
	fields := strings.SplitN(line, " ", 3)
	if len(fields) < 2 || strings.ContainsAny(line, "\r\n\t\x00") {
		return nil, ErrInvalidKey
	}
	if fields[0] != KeyType {
		return nil, ErrUnsupportedKey
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil || base64.StdEncoding.EncodeToString(blob) != fields[1] {
		return nil, ErrInvalidKey
	}
	public, err := parseWireKey(blob)
	if err != nil {
		return nil, err
	}
	return public, nil
}

// CanonicalAuthorizedKey returns the comment-free form of an authorized_keys
// line, or an error if it is not one valid ed25519 key.
func CanonicalAuthorizedKey(line string) (string, error) {
	public, err := ParseAuthorizedKey(line)
	if err != nil {
		return "", err
	}
	return AuthorizedKey(public), nil
}

func parseWireKey(blob []byte) (ed25519.PublicKey, error) {
	r := bytes.NewReader(blob)
	keyType, err := readString(r)
	if err != nil {
		return nil, ErrInvalidKey
	}
	if string(keyType) != KeyType {
		return nil, ErrUnsupportedKey
	}
	public, err := readString(r)
	if err != nil || len(public) != ed25519.PublicKeySize || r.Len() != 0 {
		return nil, ErrInvalidKey
	}
	return ed25519.PublicKey(bytes.Clone(public)), nil
}

// MarshalPrivateKey encodes an unencrypted OpenSSH private key. The comment
// must be short printable ASCII.
func MarshalPrivateKey(private ed25519.PrivateKey, comment string) ([]byte, error) {
	if len(private) != ed25519.PrivateKeySize || len(comment) > maxCommentBytes || !printable(comment) {
		return nil, ErrInvalidKey
	}
	public := private.Public().(ed25519.PublicKey)
	var check [4]byte
	if _, err := rand.Read(check[:]); err != nil {
		return nil, err
	}
	var secret bytes.Buffer
	secret.Write(check[:])
	secret.Write(check[:])
	writeString(&secret, []byte(KeyType))
	writeString(&secret, public)
	writeString(&secret, private)
	writeString(&secret, []byte(comment))
	for i := byte(1); secret.Len()%8 != 0; i++ {
		secret.WriteByte(i)
	}
	var body bytes.Buffer
	body.WriteString(privateKeyMagic)
	writeString(&body, []byte("none"))
	writeString(&body, []byte("none"))
	writeString(&body, nil)
	_ = binary.Write(&body, binary.BigEndian, uint32(1))
	writeString(&body, wireKey(public))
	writeString(&body, secret.Bytes())
	encoded := pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: body.Bytes()})
	clear(secret.Bytes())
	clear(body.Bytes())
	return encoded, nil
}

// ParsePrivateKey decodes an unencrypted OpenSSH ed25519 private key and
// checks that its public and private halves agree.
func ParsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != pemType || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrInvalidKey
	}
	r := bytes.NewReader(block.Bytes)
	magic := make([]byte, len(privateKeyMagic))
	if _, err := r.Read(magic); err != nil || string(magic) != privateKeyMagic {
		return nil, ErrInvalidKey
	}
	cipher, err1 := readString(r)
	kdf, err2 := readString(r)
	_, err3 := readString(r)
	var count uint32
	err4 := binary.Read(r, binary.BigEndian, &count)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return nil, ErrInvalidKey
	}
	if string(cipher) != "none" || string(kdf) != "none" {
		return nil, ErrEncryptedKey
	}
	if count != 1 {
		return nil, ErrInvalidKey
	}
	publicBlob, err1 := readString(r)
	secret, err2 := readString(r)
	if errors.Join(err1, err2) != nil || r.Len() != 0 || len(secret)%8 != 0 {
		return nil, ErrInvalidKey
	}
	public, err := parseWireKey(publicBlob)
	if err != nil {
		return nil, err
	}
	s := bytes.NewReader(secret)
	var check1, check2 uint32
	if binary.Read(s, binary.BigEndian, &check1) != nil || binary.Read(s, binary.BigEndian, &check2) != nil || check1 != check2 {
		return nil, ErrInvalidKey
	}
	keyType, err1 := readString(s)
	innerPublic, err2 := readString(s)
	private, err3 := readString(s)
	_, err4 = readString(s)
	if errors.Join(err1, err2, err3, err4) != nil {
		return nil, ErrInvalidKey
	}
	if string(keyType) != KeyType {
		return nil, ErrUnsupportedKey
	}
	for i := byte(1); s.Len() > 0; i++ {
		if b, _ := s.ReadByte(); b != i {
			return nil, ErrInvalidKey
		}
	}
	if len(private) != ed25519.PrivateKeySize || !bytes.Equal(innerPublic, public) || !bytes.Equal(private[32:], public) {
		return nil, ErrInvalidKey
	}
	key := ed25519.NewKeyFromSeed(private[:32])
	if !bytes.Equal(key.Public().(ed25519.PublicKey), public) {
		return nil, ErrInvalidKey
	}
	return key, nil
}

func printable(value string) bool {
	for _, c := range value {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func writeString(b *bytes.Buffer, value []byte) {
	_ = binary.Write(b, binary.BigEndian, uint32(len(value)))
	b.Write(value)
}

func readString(r *bytes.Reader) ([]byte, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, errTruncated
	}
	if int64(length) > int64(r.Len()) {
		return nil, errTruncated
	}
	value := make([]byte, length)
	if _, err := r.Read(value); err != nil && length > 0 {
		return nil, errTruncated
	}
	return value, nil
}
