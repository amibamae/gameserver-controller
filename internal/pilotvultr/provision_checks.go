package pilotvultr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
	"strings"
	"unicode/utf8"
)

// verifyBeforeDispatch runs every check a new create needs before capacity
// is claimed for dispatch.
func (p *Provisioner) verifyBeforeDispatch(ctx context.Context, token []byte, create vultrapi.CreateInstanceRequest, request CreateRequest) error {
	if err := p.verifyStartupScript(ctx, token, create.StartupScriptID, request.StartupScriptSHA256); err != nil {
		return err
	}
	if err := p.verifySSHKeys(ctx, token, request); err != nil {
		return err
	}
	rules, err := p.provider.ListFirewallRules(ctx, token, request.FirewallGroupID)
	if err != nil {
		return fmt.Errorf("%w: could not read the firewall group", ErrFirewall)
	}
	if err := vultrapi.SSHOnlyFrom(rules, request.ControllerIP); err != nil {
		return fmt.Errorf("%w: %v", ErrFirewall, err)
	}
	return nil
}

// verifySSHKeys requires exactly one ed25519 key among the create's keys,
// because the bootstrap installs exactly one, and it must be the controller's.
func (p *Provisioner) verifySSHKeys(ctx context.Context, token []byte, request CreateRequest) error {
	controller, err := sshkey.CanonicalAuthorizedKey(request.ControllerSSHPublicKey)
	if err != nil {
		return ErrSSHKeyMismatch
	}
	ed25519Keys, matches := 0, 0
	for _, id := range request.SSHKeyIDs {
		key, err := p.provider.GetSSHKey(ctx, token, id)
		if err != nil || key.ID != id {
			return fmt.Errorf("%w: could not read SSH key %s", ErrSSHKeyMismatch, id)
		}
		if !strings.HasPrefix(key.PublicKey, sshkey.KeyType+" ") {
			continue
		}
		ed25519Keys++
		if canonical, err := sshkey.CanonicalAuthorizedKey(key.PublicKey); err == nil && canonical == controller {
			matches++
		}
	}
	if ed25519Keys != 1 || matches != 1 {
		return ErrSSHKeyMismatch
	}
	return nil
}

// oneTimeHostKeyUserData is the cloud-config that installs the single-use
// host key: cloud-init deletes the image's host keys, installs this one, and
// generates no others.
func oneTimeHostKeyUserData(public []byte, private []byte) (string, error) {
	encoded, err := sshkey.MarshalPrivateKey(private, "gsc-one-time-host-key")
	if err != nil {
		return "", err
	}
	defer clear(encoded)
	var b strings.Builder
	b.WriteString("#cloud-config\nssh_deletekeys: true\nssh_genkeytypes: []\nssh_keys:\n  ed25519_private: |\n")
	for _, line := range strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	b.WriteString("  ed25519_public: " + sshkey.AuthorizedKey(public) + " gsc-one-time-host-key\n")
	return base64.StdEncoding.EncodeToString([]byte(b.String())), nil
}

func (p *Provisioner) verifyStartupScript(ctx context.Context, token []byte, scriptID, expectedDigest string) error {
	script, err := p.provider.GetStartupScript(ctx, token, scriptID)
	if err != nil || script.ID != scriptID || script.Type != "boot" {
		return ErrBootstrapScript
	}
	source, err := base64.StdEncoding.DecodeString(script.Script)
	if err != nil || !validStartupScriptSource(source) {
		return ErrBootstrapScript
	}
	digest := sha256.Sum256(source)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		return ErrBootstrapScript
	}
	return nil
}

func validStartupScriptSource(source []byte) bool {
	return len(source) > 0 && len(source) <= 1<<20 && utf8.Valid(source) && bytes.IndexByte(source, 0) < 0
}
