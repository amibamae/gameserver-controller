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
		query := url.Values{"per_page": {"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		response, err := c.request(ctx, token, http.MethodGet, "instances", query, nil, false)
		if err != nil {
			return nil, err
		}
		var envelope instanceListEnvelope
		if decodeJSON(response, &envelope) != nil || envelope.Instances == nil || envelope.Meta == nil || envelope.Meta.Total == nil || envelope.Meta.Links == nil || envelope.Meta.Links.Next == nil {
			return nil, ErrInvalidResponse
		}
		if total == -1 {
			total = *envelope.Meta.Total
		}
		if total < 0 || total > maximumPages*100 || total != *envelope.Meta.Total || len(envelope.Instances) > 100 {
			return nil, ErrInvalidResponse
		}
		for _, instance := range envelope.Instances {
			if !validResourceID(instance.ID) || ids[instance.ID] {
				return nil, ErrInvalidResponse
			}
			ids[instance.ID] = true
			instances = append(instances, instance)
		}
		cursor = *envelope.Meta.Links.Next
		if cursor == "" {
			if len(instances) != total {
				return nil, ErrInvalidResponse
			}
			return instances, nil
		}
		if len(envelope.Instances) == 0 || len(instances) >= total || len(cursor) > 2048 || cursors[cursor] {
			return nil, ErrInvalidResponse
		}
		cursors[cursor] = true
	}
	return nil, ErrInvalidResponse
}
