// Package bootstrapasset renders the reviewed Debian bootstrap asset without
// exposing deployment-specific network or storage values in the repository.
package bootstrap

import (
	"embed"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

//go:embed minecraft-debian.sh.tmpl victorialogs_export.py victorialogs_configure.py
var files embed.FS

var (
	ErrInvalidSettings = errors.New("invalid bootstrap settings")
	pathPart           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
)

// Settings contains deployment values that are intentionally absent from the
// checked-in bootstrap template. The Java heap is not one of them: the start
// script sizes it from the VM's memory, so one rendered script fits every plan.
type Settings struct {
	ControllerIP     string
	MinecraftPort    uint16
	VoiceChatPort    uint16
	WasabiRemotePath string
}

// Render validates settings and returns a complete bootstrap script.
func Render(settings Settings) ([]byte, error) {
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	template, err := files.ReadFile("minecraft-debian.sh.tmpl")
	if err != nil {
		return nil, fmt.Errorf("read embedded bootstrap template: %w", err)
	}
	shipper, err := files.ReadFile("victorialogs_export.py")
	if err != nil {
		return nil, fmt.Errorf("read embedded VictoriaLogs exporter: %w", err)
	}
	configurer, err := files.ReadFile("victorialogs_configure.py")
	if err != nil {
		return nil, fmt.Errorf("read embedded VictoriaLogs configurer: %w", err)
	}

	values := map[string]string{
		"@@CONTROLLER_IP@@":             settings.ControllerIP,
		"@@MINECRAFT_PORT@@":            strconv.Itoa(int(settings.MinecraftPort)),
		"@@VOICE_CHAT_PORT@@":           strconv.Itoa(int(settings.VoiceChatPort)),
		"@@WASABI_REMOTE_PATH@@":        settings.WasabiRemotePath,
		"@@VICTORIALOGS_EXPORT_PY@@":    strings.TrimSuffix(string(shipper), "\n"),
		"@@VICTORIALOGS_CONFIGURE_PY@@": strings.TrimSuffix(string(configurer), "\n"),
	}
	result := string(template)
	for marker, value := range values {
		result = strings.ReplaceAll(result, marker, value)
	}
	for marker := range values {
		if strings.Contains(result, marker) {
			return nil, ErrInvalidSettings
		}
	}
	return []byte(result), nil
}

// Validate rejects values that could alter shell syntax or direct firewall
// rules and storage operations to an unintended target.
func (s Settings) Validate() error {
	network, err := parseControllerIP(s.ControllerIP)
	if err != nil || !publicIPv4(network.IP) {
		return fmt.Errorf("%w: controller IP must be a public IPv4 address or CIDR", ErrInvalidSettings)
	}
	if prefix, _ := network.Mask.Size(); prefix < 16 {
		return fmt.Errorf("%w: controller IP CIDR must be /16 or narrower", ErrInvalidSettings)
	}
	if s.MinecraftPort == 0 || s.VoiceChatPort == 0 || s.MinecraftPort == s.VoiceChatPort {
		return fmt.Errorf("%w: Minecraft and voice chat ports must be distinct nonzero ports", ErrInvalidSettings)
	}
	if !safeRemotePath(s.WasabiRemotePath) {
		return fmt.Errorf("%w: Wasabi remote path must be a bucket name with an optional safe prefix", ErrInvalidSettings)
	}
	return nil
}

// publicIPv4 reports whether ip is a global unicast IPv4 address that is not
// private, loopback or link-local.
func publicIPv4(ip net.IP) bool {
	if ip.To4() == nil || !ip.IsGlobalUnicast() {
		return false
	}
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

// safeRemotePath reports whether path is a bucket name with an optional
// prefix: allowed characters only, no "..", no empty segment, and no slash at
// either end.
func safeRemotePath(path string) bool {
	if !pathPart.MatchString(path) || strings.Contains(path, "..") {
		return false
	}
	return !strings.Contains(path, "//") && !strings.HasPrefix(path, "/") && !strings.HasSuffix(path, "/")
}

// parseControllerIP accepts a bare IPv4 address (treated as /32) or a CIDR.
func parseControllerIP(value string) (*net.IPNet, error) {
	if !strings.Contains(value, "/") {
		ip := net.ParseIP(value).To4()
		if ip == nil {
			return nil, errors.New("not an IPv4 address")
		}
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}, nil
	}
	_, network, err := net.ParseCIDR(value)
	if err != nil {
		return nil, err
	}
	return network, nil
}
