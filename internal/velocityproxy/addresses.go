package velocityproxy

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// AddressMode selects how the proxy reaches one backend.
type AddressMode string

const (
	// AddressInstanceIP connects to the instance's current public IP, as
	// reported by the hosting provider (for Vultr, the instance's main_ip).
	AddressInstanceIP AddressMode = "instance_ip"
	// AddressHostname connects to a fixed DNS name, such as a managed
	// subdomain kept pointed at the instance. The proxy configuration then
	// does not change when the instance's IP changes.
	AddressHostname AddressMode = "hostname"
)

// BackendAddressConfig is the per-backend address setting.
type BackendAddressConfig struct {
	Mode     AddressMode
	Hostname string // required for AddressHostname
	Port     uint16
}

// InstanceIPLookup returns the current public IP of a hosting instance. It
// must return an error while the provider has not assigned an IP yet.
type InstanceIPLookup func(ctx context.Context, instance ResourceRef) (netip.Addr, error)

// ConfiguredAddresses resolves backend addresses from per-backend settings.
type ConfiguredAddresses struct {
	Backends map[string]BackendAddressConfig
	LookupIP InstanceIPLookup
}

var hostnamePattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))+$`)

// Validate checks every backend setting.
func (c ConfiguredAddresses) Validate() error {
	if len(c.Backends) == 0 {
		return errors.New("at least one backend address setting is required")
	}
	for _, setting := range c.Backends {
		if setting.Port == 0 {
			return errors.New("backend address port is required")
		}
		switch setting.Mode {
		case AddressInstanceIP:
			if c.LookupIP == nil {
				return errors.New("instance IP backends require an instance IP lookup")
			}
		case AddressHostname:
			if !validHostname(setting.Hostname) {
				return errors.New("hostname backends require a valid DNS hostname")
			}
		default:
			return errors.New("backend address mode must be instance_ip or hostname")
		}
	}
	return nil
}

// ResolveBackendAddress implements AddressResolver.
func (c ConfiguredAddresses) ResolveBackendAddress(ctx context.Context, target BackendTarget) (string, error) {
	setting, ok := c.Backends[target.BackendID]
	if !ok || setting.Port == 0 {
		return "", errors.New("backend has no address setting")
	}
	port := strconv.Itoa(int(setting.Port))
	switch setting.Mode {
	case AddressHostname:
		if !validHostname(setting.Hostname) {
			return "", errors.New("backend hostname is invalid")
		}
		return net.JoinHostPort(strings.ToLower(strings.TrimSuffix(setting.Hostname, ".")), port), nil
	case AddressInstanceIP:
		if c.LookupIP == nil {
			return "", errors.New("instance IP lookup is not configured")
		}
		ip, err := c.LookupIP(ctx, target.InstanceRef)
		if err != nil {
			return "", errors.New("instance IP is unavailable")
		}
		if !usableBackendIP(ip) {
			return "", errors.New("instance IP is not assigned")
		}
		return net.JoinHostPort(ip.Unmap().String(), port), nil
	default:
		return "", errors.New("backend address mode is invalid")
	}
}

func validHostname(value string) bool {
	value = strings.TrimSuffix(value, ".")
	return len(value) <= 253 && hostnamePattern.MatchString(value) && net.ParseIP(value) == nil
}

// usableBackendIP rejects the unspecified address Vultr reports before an
// instance has an IP, plus loopback, link-local, and multicast addresses.
func usableBackendIP(ip netip.Addr) bool {
	return ip.IsValid() && !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast()
}

// sameAddress compares host:port addresses, ignoring hostname case and a
// trailing dot.
func sameAddress(observed, current string) bool {
	oh, op, err1 := net.SplitHostPort(observed)
	ch, cp, err2 := net.SplitHostPort(current)
	if err1 != nil || err2 != nil || op != cp {
		return false
	}
	normalize := func(host string) string {
		if ip, err := netip.ParseAddr(host); err == nil {
			return ip.Unmap().String()
		}
		return strings.ToLower(strings.TrimSuffix(host, "."))
	}
	return normalize(oh) == normalize(ch)
}
