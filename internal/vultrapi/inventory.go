package vultrapi

import (
	"context"
	"net/http"
	"net/url"
)

// ListInstances reads the complete unfiltered account inventory. Missing totals,
// changing totals, duplicate IDs, cursor loops, and truncated pages invalidate
// the entire observation; partial inventory is never returned as success.
// Pagination is observational, not an atomic snapshot of the provider account.
func (c *Client) ListInstances(ctx context.Context, token []byte) ([]Instance, error) {
	instances := make([]Instance, 0)
	ids, cursors := map[string]bool{}, map[string]bool{}
	cursor, total := "", -1
	for page := 0; page < maximumPages; page++ {
		observation, err := c.readInventoryPage(ctx, token, cursor)
		if err != nil {
			return nil, err
		}
		total, err = observation.validateTotal(total)
		if err != nil {
			return nil, err
		}
		instances, err = appendInventoryInstances(instances, observation.instances, ids)
		if err != nil {
			return nil, err
		}
		cursor = observation.next
		if cursor == "" {
			if len(instances) != total {
				return nil, ErrInvalidResponse
			}
			return instances, nil
		}
		if !observation.canContinue(len(instances), total, cursors) {
			return nil, ErrInvalidResponse
		}
		cursors[cursor] = true
	}
	return nil, ErrInvalidResponse
}

type inventoryPage struct {
	instances []Instance
	total     int
	next      string
}

func (c *Client) readInventoryPage(ctx context.Context, token []byte, cursor string) (inventoryPage, error) {
	query := url.Values{"per_page": {"100"}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	response, err := c.request(ctx, token, http.MethodGet, "instances", query, nil, false)
	if err != nil {
		return inventoryPage{}, err
	}
	var envelope instanceListEnvelope
	if decodeJSON(response, &envelope) != nil || envelope.Instances == nil || !hasInventoryMetadata(envelope.Meta) {
		return inventoryPage{}, ErrInvalidResponse
	}
	return inventoryPage{instances: envelope.Instances, total: *envelope.Meta.Total, next: *envelope.Meta.Links.Next}, nil
}

func hasInventoryMetadata(meta *instanceListMeta) bool {
	return meta != nil && meta.Total != nil && meta.Links != nil && meta.Links.Next != nil
}

func (page inventoryPage) validateTotal(previous int) (int, error) {
	if previous == -1 {
		previous = page.total
	}
	if previous < 0 || previous > maximumPages*100 || previous != page.total || len(page.instances) > 100 {
		return 0, ErrInvalidResponse
	}
	return previous, nil
}

func (page inventoryPage) canContinue(observed, total int, seenCursors map[string]bool) bool {
	return len(page.instances) > 0 && observed < total && len(page.next) <= 2048 && !seenCursors[page.next]
}

func appendInventoryInstances(instances, next []Instance, seenIDs map[string]bool) ([]Instance, error) {
	for _, instance := range next {
		if !validResourceID(instance.ID) || seenIDs[instance.ID] {
			return nil, ErrInvalidResponse
		}
		seenIDs[instance.ID] = true
		instances = append(instances, instance)
	}
	return instances, nil
}
