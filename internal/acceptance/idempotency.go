package acceptance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gowebpki/jcs"

	"github.com/amibamae/gameserver-controller/internal/store"
)

var ErrInvalidKeyring = errors.New("invalid idempotency keyring")

// Keyring holds versioned installation secrets used to create opaque,
// privacy-minimized idempotency-key verifiers. Keep all versions referenced by
// the database until the installation is permanently retired.
type Keyring struct {
	activeVersion string
	keys          map[string][]byte
}

// NewKeyring copies key material so later caller mutations cannot change the
// verifier behavior of an initialized controller.
func NewKeyring(activeVersion string, keys map[string][]byte) (*Keyring, error) {
	if !validKeyVersion(activeVersion) {
		return nil, fmt.Errorf("%w: invalid active version", ErrInvalidKeyring)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no keys supplied", ErrInvalidKeyring)
	}
	keyring := &Keyring{activeVersion: activeVersion, keys: make(map[string][]byte, len(keys))}
	for version, key := range keys {
		if !validKeyVersion(version) || len(key) < 32 {
			return nil, fmt.Errorf("%w: version %q must have at least 32 bytes of key material", ErrInvalidKeyring, version)
		}
		keyring.keys[version] = append([]byte(nil), key...)
	}
	if _, ok := keyring.keys[activeVersion]; !ok {
		return nil, fmt.Errorf("%w: active key %q is missing", ErrInvalidKeyring, activeVersion)
	}
	return keyring, nil
}

// RequestFingerprint returns the SHA-256 digest of RFC 8785 canonical JSON
// request parameters. The protocol defines parameters as a JSON object.
func RequestFingerprint(parameters json.RawMessage) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(parameters, &object); err != nil || object == nil {
		return "", errors.New("request parameters must be a JSON object")
	}
	canonical, err := jcs.Transform(parameters)
	if err != nil {
		return "", fmt.Errorf("canonicalize request parameters: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// Proof creates lookup verifiers for every retained key version and identifies
// the active verifier to persist for a new operation. The canonical request
// binding is independently HMACed so the index does not retain request data.
func (k *Keyring) Proof(actorID, idempotencyKey string, binding []byte) (store.IdempotencyProof, error) {
	if k == nil || actorID == "" || len(actorID) > 200 || len(idempotencyKey) < 1 || len(idempotencyKey) > 128 || len(binding) == 0 {
		return store.IdempotencyProof{}, errors.New("actor and idempotency key are required")
	}
	for i := range idempotencyKey {
		if idempotencyKey[i] < 0x21 || idempotencyKey[i] > 0x7e {
			return store.IdempotencyProof{}, errors.New("idempotency key must use visible ASCII")
		}
	}
	versions := make([]string, 0, len(k.keys))
	for version := range k.keys {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	proof := store.IdempotencyProof{ActiveKeyVersion: k.activeVersion}
	for _, version := range versions {
		digest := keyVerifier(k.keys[version], actorID, idempotencyKey)
		bindingDigest := keyBindingDigest(k.keys[version], binding)
		proof.Candidates = append(proof.Candidates, store.IdempotencyVerifier{KeyVersion: version, Digest: digest[:], BindingDigest: bindingDigest[:]})
	}
	return proof, nil
}

func keyVerifier(secret []byte, actorID, idempotencyKey string) [sha256.Size]byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("gameserver-controller/idempotency-key/v1\x00"))
	writeLengthPrefixed(mac, []byte(actorID))
	writeLengthPrefixed(mac, []byte(idempotencyKey))
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func keyBindingDigest(secret, binding []byte) [sha256.Size]byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("gameserver-controller/idempotency-binding/v1\x00"))
	writeLengthPrefixed(mac, binding)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func writeLengthPrefixed(mac interface{ Write([]byte) (int, error) }, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = mac.Write(length[:])
	_, _ = mac.Write(value)
}

func validKeyVersion(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	for _, r := range version {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("._-", r) {
			return false
		}
	}
	return true
}
