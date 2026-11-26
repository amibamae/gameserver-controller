package velocityproxy

import (
	"encoding/json"
	"errors"
)

// decodeTargeted decodes a backend capability input and rejects a target
// outside this adapter's environment.
func decodeTargeted[T interface{ backendTarget() BackendTarget }](a *Adapter, raw json.RawMessage) (T, error) {
	var input T
	if err := decode(raw, &input); err != nil {
		return input, err
	}
	return input, a.checkTarget(input.backendTarget())
}

// decodeForProxy decodes a session or cohort input and rejects one that names
// a proxy instance other than this adapter's; what names the inspection in
// that error.
func decodeForProxy[T interface{ proxyRef() ResourceRef }](a *Adapter, raw json.RawMessage, what string) (T, error) {
	var input T
	if err := decode(raw, &input); err != nil {
		return input, err
	}
	if input.proxyRef() != a.config.ProxyInstanceRef {
		return input, errors.New(what + " inspection targets a different proxy instance")
	}
	return input, nil
}

func decode(input json.RawMessage, target any) error {
	if err := json.Unmarshal(input, target); err != nil {
		return errors.New("invalid Velocity capability input")
	}
	return nil
}

// checkTarget rejects targets outside this adapter's environment. The
// registry has already validated the input schema.
func (a *Adapter) checkTarget(target BackendTarget) error {
	if target.EnvironmentID != a.config.EnvironmentID || target.InstanceRef.EnvironmentID != target.EnvironmentID {
		return errors.New("backend target is outside this Velocity adapter's environment")
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		if seen[value] == 0 {
			return false
		}
		seen[value]--
	}
	return true
}

// uniqueSessionIDs reports whether every ID is non-empty, at most 200 bytes,
// and listed once.
func uniqueSessionIDs(ids []string) bool {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 200 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
