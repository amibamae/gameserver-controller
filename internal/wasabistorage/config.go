package wasabistorage

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/credential"
)

var (
	adapterIDPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{1,159}$`)
	regionPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// validateConfig checks a profile in four stages, each with its own error:
// identity and object location, the object size bound, the credential
// references, and the endpoint.
func validateConfig(config Config) error {
	if !validProfileIdentity(config) {
		return errors.New("storage profile, environment, container, key prefix, and Wasabi region are required")
	}
	if !validObjectLimit(config.MaxObjectBytes) {
		return errors.New("maximum object size must be between 1 byte and the bounded multipart limit")
	}
	if !separateSystemdReferences(config.AccessKeyIDRef, config.SecretAccessKeyRef) {
		return errors.New("separate systemd credential references for both Wasabi keys are required")
	}
	return validateEndpoint(config.EndpointURL)
}

func validProfileIdentity(config Config) bool {
	return validAdapterID(config.AdapterID) && profileScoped(config) && validObjectLocation(config)
}

func validAdapterID(id string) bool {
	return adapterIDPattern.MatchString(id) && strings.HasPrefix(id, "storage.")
}

func profileScoped(config Config) bool {
	return config.StorageProfileID != "" && config.EnvironmentID != ""
}

func validObjectLocation(config Config) bool {
	return validContainerName(config.Container) && validKeyPrefix(config.KeyPrefix) && regionPattern.MatchString(config.Region)
}

func validContainerName(container string) bool {
	return container != "" && len(container) <= 500
}

func validKeyPrefix(prefix string) bool {
	return strings.HasSuffix(prefix, "/") && len(prefix) <= 2048
}

// validObjectLimit bounds the profile's object size by both S3's object limit
// and what the bounded part size can upload in the allowed number of parts.
func validObjectLimit(maximum int64) bool {
	return maximum >= 1 && maximum <= maxS3ObjectSize && maximum <= maxPartSize*maxMultipartParts
}

func separateSystemdReferences(access, secret string) bool {
	return systemdReference(access) && systemdReference(secret) && access != secret
}

func systemdReference(reference string) bool {
	return strings.HasPrefix(reference, credential.SystemdReferenceScheme)
}

func validateEndpoint(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || !plainHTTPSURL(endpoint) {
		return errors.New("Wasabi endpoint must be an HTTPS URL without user information, query, or fragment")
	}
	if !wasabiHost(endpoint.Hostname()) {
		return errors.New("Wasabi endpoint host must be within wasabisys.com")
	}
	if !defaultHTTPSPort(endpoint.Port()) {
		return errors.New("Wasabi endpoint must use HTTPS port 443")
	}
	return nil
}

// plainHTTPSURL reports an https URL with a host and nothing a client could
// use to change where or as whom it connects.
func plainHTTPSURL(endpoint *url.URL) bool {
	return endpoint.Scheme == "https" && endpoint.User == nil && endpoint.Hostname() != "" && bareLocation(endpoint)
}

func bareLocation(endpoint *url.URL) bool {
	return endpoint.RawQuery == "" && endpoint.Fragment == "" && rootPath(endpoint.Path)
}

func rootPath(path string) bool {
	return path == "" || path == "/"
}

// wasabiHost reports a DNS name, not an IP address, at or below wasabisys.com.
func wasabiHost(hostname string) bool {
	host := strings.TrimSuffix(strings.ToLower(hostname), ".")
	return net.ParseIP(host) == nil && (host == "wasabisys.com" || strings.HasSuffix(host, ".wasabisys.com"))
}

func defaultHTTPSPort(port string) bool {
	return port == "" || port == "443"
}
