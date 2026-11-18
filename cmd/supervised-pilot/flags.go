package main

import (
	"github.com/amibamae/gameserver-controller/internal/credential"
)

// createVultrCredential returns the default Vultr API token credential.
func createVultrCredential() string {
	return credential.SystemdReferenceScheme + "vultr-pilot-api-token"
}

// createSSHCredential returns the default SSH key credential reference.
func createSSHCredential() string {
	return credential.SystemdReferenceScheme + "gsc-pilot-ssh-key"
}
