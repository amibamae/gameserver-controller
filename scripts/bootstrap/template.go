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

//go:embed minecraft-debian.sh.tmpl victorialogs_export.py victorialogs_configure.py minecraft_probe.py
var files embed.FS

var (
	ErrInvalidSettings = errors.New("invalid bootstrap settings")
	pathPart           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
)

// VultrPlan constants map plan name to RAM for heap configuration.
const (
	vultrVC2  = 2 * 1024 * 1024 * 1024  // 2 GiB
	vultrVC4  = 4 * 1024 * 1024 * 1024  // 4 GiB
	vultrVC6  = 6 * 1024 * 1024 * 1024  // 6 GiB
	vultrVC8  = 8 * 1024 * 1024 * 1024  // 8 GiB
	vultrVC10 = 10 * 1024 * 1024 * 1024 // 10 GiB
	vultrVC11 = 11 * 1024 * 1024 * 1024 // 11 GiB
	vultrVC12 = 12 * 1024 * 1024 * 1024 // 12 GiB
	vultrVC16 = 16 * 1024 * 1024 * 1024 // 16 GiB
	vultrVC32 = 32 * 1024 * 1024 * 1024 // 32 GiB
)

// vultrPlanToRAM maps plan name to RAM in bytes.
var vultrPlanToRAM = map[string]uint64{
	"vc2-1-0150":  vultrVC2,
	"vc4-1-0150":  vultrVC4,
	"vc6-1-0150":  vultrVC6,
	"vc8-1-0150":  vultrVC8,
	"vc10-1-0150": vultrVC10,
	"vc11-1-0150": vultrVC11,
	"vc12-1-0150": vultrVC12,
	"vc16-1-0150": vultrVC16,
	"vc32-1-0150": vultrVC32,
}

// Settings contains deployment values that are intentionally absent from the
// checked-in bootstrap template.
type Settings struct {
	ControllerIP     string
	MinecraftPort    uint16
	VoiceChatPort    uint16
	WasabiRemotePath string
	Plan             string
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
	probe, err := files.ReadFile("minecraft_probe.py")
	if err != nil {
		return nil, fmt.Errorf("read embedded Minecraft health probe: %w", err)
	}

	// Get heap values from plan; default to 2GB min/max if plan unknown.
	planRAM, ok := vultrPlanToRAM[settings.Plan]
	if !ok || planRAM == 0 {
		planRAM = vultrVC2 // fallback to vc2 size
	}
	// Use 50% of plan RAM for min heap, same as max for simplicity.
	heapMin := int(planRAM / 2)
	heapMax := int(planRAM)

	values := map[string]string{
		"@@CONTROLLER_IP@@":             settings.ControllerIP,
		"@@MINECRAFT_PORT@@":            strconv.Itoa(int(settings.MinecraftPort)),
		"@@VOICE_CHAT_PORT@@":           strconv.Itoa(int(settings.VoiceChatPort)),
		"@@WASABI_REMOTE_PATH@@":        settings.WasabiRemotePath,
		"@@JAVA_HEAP_MIN@@":             strconv.Itoa(heapMin),
		"@@JAVA_HEAP_MAX@@":             strconv.Itoa(heapMax),
		"@@VICTORIALOGS_EXPORT_PY@@":    strings.TrimSuffix(string(shipper), "\n"),
		"@@VICTORIALOGS_CONFIGURE_PY@@": strings.TrimSuffix(string(configurer), "\n"),
		"@@MINECRAFT_PROBE_PY@@":        strings.TrimSuffix(string(probe), "\n"),
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
	if err != nil || network.IP.To4() == nil || !network.IP.IsGlobalUnicast() || network.IP.IsPrivate() || network.IP.IsLoopback() || network.IP.IsLinkLocalUnicast() {
		return fmt.Errorf("%w: controller IP must be a public IPv4 address or CIDR", ErrInvalidSettings)
	}
	if prefix, _ := network.Mask.Size(); prefix < 16 {
		return fmt.Errorf("%w: controller IP CIDR must be /16 or narrower", ErrInvalidSettings)
	}
	if s.MinecraftPort == 0 || s.VoiceChatPort == 0 || s.MinecraftPort == s.VoiceChatPort {
		return fmt.Errorf("%w: Minecraft and voice chat ports must be distinct nonzero ports", ErrInvalidSettings)
	}
	path := s.WasabiRemotePath
	if !pathPart.MatchString(path) || strings.Contains(path, "..") || strings.Contains(path, "//") || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("%w: Wasabi remote path must be a bucket name with an optional safe prefix", ErrInvalidSettings)
	}
	if s.Plan == "" {
		return fmt.Errorf("%w: Vultr plan must be specified (e.g., vc2-1-0150, vc4-1-0150)", ErrInvalidSettings)
	}
	return nil
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
