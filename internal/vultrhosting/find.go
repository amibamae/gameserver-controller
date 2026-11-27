package vultrhosting

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

// maxMatches is enough to tell zero, one, and duplicate instances apart.
const maxMatches = 2

var resourceKeyPattern = regexp.MustCompile(`^gsc-[a-f0-9]{64}$`)

type findInput struct {
	ResourceKey string `json:"resource_key"`
}

type findMatch struct {
	InstanceRef instanceRef `json:"instance_ref"`
	State       string      `json:"state"`
}

type findOutput struct {
	ResourceKey       string      `json:"resource_key"`
	InventoryComplete bool        `json:"inventory_complete"`
	Matches           []findMatch `json:"matches"`
	ObservedAt        string      `json:"observed_at"`
}

// findByResourceKey looks up instances whose Vultr label is exactly the
// controller resource key that hosting.instance.create sets. The client reads
// every page or fails, so a result is always a complete inventory.
func (a *Adapter) findByResourceKey(ctx context.Context, input json.RawMessage) (any, error) {
	var request findInput
	if err := decodeStrict(input, &request); err != nil || !resourceKeyPattern.MatchString(request.ResourceKey) {
		return nil, errors.New("invalid Vultr resource key lookup input")
	}
	var instances []vultrapi.Instance
	err := a.withToken(func(token []byte) error {
		var listErr error
		instances, listErr = a.client.ListInstancesByLabel(ctx, token, request.ResourceKey)
		return listErr
	})
	if err != nil {
		return nil, errors.New("Vultr inventory lookup failed; matches are unknown")
	}
	return findOutput{ResourceKey: request.ResourceKey, InventoryComplete: true,
		Matches: a.matches(instances, request.ResourceKey), ObservedAt: a.observedAt()}, nil
}

// matches keeps distinct instances whose label is exactly key, at most two.
func (a *Adapter) matches(instances []vultrapi.Instance, key string) []findMatch {
	matches := []findMatch{}
	seen := map[string]bool{}
	for _, instance := range instances {
		if instance.Label != key || instance.ID == "" || seen[instance.ID] {
			continue
		}
		seen[instance.ID] = true
		ref := instanceRef{AdapterID: a.config.AdapterID, EnvironmentID: a.config.EnvironmentID, ResourceKind: resourceKind, ExternalID: instance.ID}
		matches = append(matches, findMatch{InstanceRef: ref, State: instanceState(instance)})
		if len(matches) == maxMatches {
			break
		}
	}
	return matches
}
