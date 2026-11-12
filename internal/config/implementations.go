package config

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Implementation describes one adapter implementation an adapter profile can
// select with `implementation: <Name>`. Each implementation lives in its own
// file and registers itself with RegisterImplementation, so adding or
// removing an adapter is adding or removing that file. Its settings block is
// the profile key of the same name.
type Implementation struct {
	// Name is the implementation name and the settings block key.
	Name string
	// Category, when set, is the only adapter category that may use it.
	Category string
	// Secrets, when non-nil, lists exactly the secret_refs names the profile
	// must define.
	Secrets []string
	// NewSettings returns a pointer to an empty settings value. The settings
	// block is decoded into it with unknown fields rejected.
	NewSettings func() ImplementationSettings
}

// ImplementationSettings is the decoded settings block of one adapter
// profile.
type ImplementationSettings interface {
	// Validate reports problems through check. It must not touch the network,
	// the filesystem, or credentials.
	Validate(check SettingsCheck)
}

// EnvironmentBound is implemented by settings that bind an adapter to one
// environment. Servers in other environments may not use such an adapter.
type EnvironmentBound interface {
	BoundEnvironment() string
}

// DNSZoneOwner is implemented by DNS adapter settings. Managed hostnames of
// the servers that use the adapter must lie inside the zone.
type DNSZoneOwner interface {
	DNSZone() string
}

// SettingsCheck collects problems for one profile's settings block.
type SettingsCheck struct {
	label        string
	problems     *[]string
	environments map[string]Environment
	// Profile is the profile being validated.
	Profile AdapterProfile
}

// Require records problem, prefixed with the settings block path, unless ok.
func (c SettingsCheck) Require(ok bool, problem string) {
	if !ok {
		*c.problems = append(*c.problems, c.label+"."+c.Profile.Implementation+"."+problem)
	}
}

// KnownEnvironment reports whether id names a configured environment.
func (c SettingsCheck) KnownEnvironment(id string) bool {
	_, ok := c.environments[id]
	return ok
}

var (
	implementationsMu  sync.RWMutex
	implementations    = map[string]Implementation{}
	implementationName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	reservedProfileKey = map[string]bool{"id": true, "category": true, "secret_refs": true, "implementation": true}
)

// RegisterImplementation makes an implementation available. It panics on an
// invalid or duplicate registration, which is a programming error.
func RegisterImplementation(implementation Implementation) {
	if !implementationName.MatchString(implementation.Name) || reservedProfileKey[implementation.Name] || implementation.NewSettings == nil {
		panic("config: invalid adapter implementation registration " + implementation.Name)
	}
	implementationsMu.Lock()
	defer implementationsMu.Unlock()
	if _, exists := implementations[implementation.Name]; exists {
		panic("config: duplicate adapter implementation " + implementation.Name)
	}
	implementations[implementation.Name] = implementation
}

// LookupImplementation returns a registered implementation.
func LookupImplementation(name string) (Implementation, bool) {
	implementationsMu.RLock()
	defer implementationsMu.RUnlock()
	implementation, ok := implementations[name]
	return implementation, ok
}

// ImplementationNames lists the registered implementations in name order.
func ImplementationNames() []string {
	implementationsMu.RLock()
	defer implementationsMu.RUnlock()
	names := make([]string, 0, len(implementations))
	for name := range implementations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// SettingsOf returns a profile's decoded settings as type T.
func SettingsOf[T ImplementationSettings](profile AdapterProfile) (T, bool) {
	settings, ok := profile.Settings.(T)
	return settings, ok
}

// BoundEnvironment returns the environment the profile's adapter is bound
// to, or "" when it is not environment-bound.
func BoundEnvironment(profile AdapterProfile) string {
	if bound, ok := profile.Settings.(EnvironmentBound); ok {
		return bound.BoundEnvironment()
	}
	return ""
}

// resolveSettings decodes the profile's settings block for its implementation.
func resolveSettings(profile *AdapterProfile) error {
	if profile.Implementation == "" || profile.Settings != nil {
		return nil
	}
	implementation, ok := LookupImplementation(profile.Implementation)
	if !ok {
		return errUnknownImplementation
	}
	block, ok := profile.Blocks[profile.Implementation]
	if !ok {
		return errMissingBlock
	}
	settings := implementation.NewSettings()
	data, err := yaml.Marshal(&block)
	if err != nil {
		return errInvalidBlock
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(settings); err != nil {
		// Decoder errors can quote values; keep them out of messages.
		return errInvalidBlock
	}
	profile.Settings = settings
	return nil
}

var (
	errUnknownImplementation = errors.New("unknown implementation")
	errMissingBlock          = errors.New("missing settings block")
	errInvalidBlock          = errors.New("invalid settings block")
)

// validateImplementation checks the profile's settings blocks, its
// implementation's category and secret names, and the settings themselves.
func validateImplementation(problems *[]string, label string, profile AdapterProfile, environments map[string]Environment) {
	add := func(ok bool, message string) {
		if !ok {
			*problems = append(*problems, label+message)
		}
	}
	for key := range profile.Blocks {
		switch _, registered := LookupImplementation(key); {
		case key == profile.Implementation:
		case registered && profile.Implementation == "":
			add(false, " has "+key+" settings but no implementation")
		case registered:
			add(false, " must not have "+key+" settings with implementation "+profile.Implementation)
		default:
			add(false, " has unknown field "+key)
		}
	}
	if profile.Implementation == "" {
		return
	}
	implementation, ok := LookupImplementation(profile.Implementation)
	if !ok {
		add(false, ".implementation must be one of "+strings.Join(ImplementationNames(), ", ")+" when set")
		return
	}
	if implementation.Category != "" {
		add(profile.Category == implementation.Category, ".implementation "+implementation.Name+" requires category "+implementation.Category)
	}
	if implementation.Secrets != nil {
		add(exactSecretNames(profile.SecretRefs, implementation.Secrets), ".secret_refs must define exactly "+strings.Join(implementation.Secrets, " and ")+" for implementation "+implementation.Name)
	}
	switch err := resolveSettings(&profile); {
	case errors.Is(err, errMissingBlock):
		add(false, "."+implementation.Name+" is required with implementation "+implementation.Name)
		return
	case err != nil:
		add(false, "."+implementation.Name+" has an unknown field or a value of the wrong type")
		return
	}
	profile.Settings.Validate(SettingsCheck{label: label, problems: problems, environments: environments, Profile: profile})
}

func exactSecretNames(refs map[string]SecretReference, names []string) bool {
	if len(refs) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := refs[name]; !ok {
			return false
		}
	}
	return true
}

// ValidDNSHostname reports whether value is a lower-case DNS hostname that is
// not an IP literal.
func ValidDNSHostname(value string) bool { return validDNSHostname(value) }

func profileLabel(source string, index int) string {
	if source != "" {
		return fmt.Sprintf("adapters.d/%s", source)
	}
	return fmt.Sprintf("adapters[%d]", index)
}
