package controllersetup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/config"
)

// VerifyStoredCredentials checks, before any host change, that every
// credential the configuration references is already stored as a private
// encrypted file in the credential directory, where the service unit's
// LoadCredentialEncrypted= lines will look for it. root maps the absolute
// credential directory for tests; production passes "/". It reads file
// metadata only, never credential contents.
func VerifyStoredCredentials(value config.Config, root, credentialDir string) error {
	if !filepath.IsAbs(credentialDir) || filepath.Clean(credentialDir) != credentialDir {
		return errors.New("credential directory must be clean and absolute")
	}
	directory := mapRootPath(root, credentialDir)
	var missing []string
	for _, name := range ConfiguredCredentialNames(value) {
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil || !storedCredentialUsable(info) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return errors.New("store these configured credentials with `controller-setup credentials set` or `credentials generate` before installing; each must be a non-empty regular file with no group or other permissions in " +
			credentialDir + ": " + strings.Join(missing, ", "))
	}
	return nil
}

// storedCredentialUsable reports a non-empty regular file (not a symlink)
// that only its owner can access, as `credentials set` leaves it.
func storedCredentialUsable(info fs.FileInfo) bool {
	return info.Mode().IsRegular() && info.Size() > 0 && info.Mode().Perm()&0o077 == 0
}
