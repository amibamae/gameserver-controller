// Package vultrhosting implements a Vultr hosting adapter for the controller runtime.
package vultrhosting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

var _ adapter.Implementation = (*vultrAdapter)(nil)

type vultrAdapter struct {
	mu                 sync.RWMutex
	apiToken           string
	httpClient         *http.Client
	descriptor         adapter.Descriptor
	capabilities       map[string]adapter.Capability
	baseURL            string
	invalidRequests    chan<- interface{} // for invalid request monitoring
}

// New creates a Vultr hosting adapter.
func New(invalidRequests <-chan interface{}) (adapter.Implementation, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	tokenSource := ""

	return &vultrAdapter{
		apiToken:         tokenSource,
		httpClient:       client,
		capabilities:     make(map[string]adapter.Capability),
		baseURL:          "https://api.vultr.com",
		invalidRequests:  invalidRequests,
	}, nil
}

// Descriptor returns the adapter descriptor.
func (a *vultrAdapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		AdapterID:            "vultrhosting",
		Category:             adapter.CategoryHosting,
		DisplayName:          "Vultr Hosting Adapter",
		ImplementationVersion: "1.0.0",
		ProtocolVersions:     []string{"0.21.0-draft"},
		Capabilities: []adapter.Capability{
			{
				Name:               "hosting.instance-create",
				Version:            "1.0",
				Mutating:           true,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:instance-create-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:instance-create-response",
			},
			{
				Name:               "hosting.instance-get",
				Version:            "1.0",
				Mutating:           false,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:instance-get-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:instance-get-response",
			},
			{
				Name:               "hosting.instance-list",
				Version:            "1.0",
				Mutating:           false,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:instance-list-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:instance-list-response",
			},
			{
				Name:               "hosting.instance-delete",
				Version:            "1.0",
				Mutating:           true,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:instance-delete-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:instance-delete-response",
			},
			{
				Name:               "hosting.instance-start",
				Version:            "1.0",
				Mutating:           true,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:instance-start-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:instance-start-response",
			},
			{
				Name:               "hosting.instance-stop",
				Version:            "1.0",
				Mutating:           true,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:instance-stop-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:instance-stop-response",
			},
			{
				Name:               "hosting.instance-reboot",
				Version:            "1.0",
				Mutating:           true,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-response",
			},
			{
				Name:               "hosting.region-list",
				Version:            "1.0",
				Mutating:           false,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:region-list-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:region-list-response",
			},
			{
				Name:               "hosting.plan-list",
				Version:            "1.0",
				Mutating:           false,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:plan-list-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:plan-list-response",
			},
			{
				Name:               "hosting.os-list",
				Version:            "1.0",
				Mutating:           false,
				InputSchemaURI:     "urn:gameserver-controller:0.21.0-draft:hosting:os-list-request",
				OutputSchemaURI:    "urn:gameserver-controller:0.21.0-draft:hosting:os-list-response",
			},
		},
	}
}

// Invoke implements the adapter interface for all Vultr hosting capabilities.
func (a *vultrAdapter) Invoke(ctx context.Context, schemaURI string, body json.RawMessage) (json.RawMessage, error) {
	a.mu.RLock()
	capabilities := a.capabilities
	muRlck := a.mu
	a.mu.RUnlock()

	if capabilities == nil {
		return nil, fmt.Errorf("capabilities not initialized")
	}

	for _, cap := range capabilities {
		if cap.InputSchemaURI == schemaURI {
			methodPath := getVultrEndpoint(cap.Name)
			resp, err := a.invoke(ctx, methodPath, body)
			if err != nil {
				return nil, err
			}

			resultBody, err := a.handleResponse(resp, methodPath, 10*1024*1024)
			if err != nil {
				// Report to monitoring channel if invalid requests should be tracked
				select {
				case a.invalidRequests <- struct{}{}:
				default:
				}
				return nil, err
			}

			// For list operations, extract the items array
			if cap.Name == "hosting.instance-list" || cap.Name == "hosting.region-list" || cap.Name == "hosting.plan-list" || cap.Name == "hosting.os-list" {
				var envelope map[string]any
				if err := json.Unmarshal(resultBody, &envelope); err != nil {
					return nil, fmt.Errorf("unmarshal response envelope: %w", err)
				}

				if items, exists := envelope["data"]; exists {
					if itemsArray, ok := items.(map[string]any); ok {
						return json.Marshal(itemsArray)
					}
				}

				// Fall back to parsing the envelope directly
				return a.parseEnvelope(resultBody, strings.TrimPrefix(cap.Name, "hosting."))
			}

			return resultBody, nil
		}
	}

	return nil, &adapter.InvocationError{MayHaveSideEffects: false, Cause: fmt.Errorf("no capability found for schema URI %s", schemaURI)}
}

// getVultrEndpoint maps adapter capability names to Vultr API endpoints.
func getVultrEndpoint(capName string) string {
	endpoints := map[string]string{
		"hosting.instance-create":   "/v2/instances",
		"hosting.instance-get":      "/v2/instance/{UUID}",
		"hosting.instance-list":     "/v2/instances",
		"hosting.instance-delete":   "/v2/instance/{UUID}",
		"hosting.instance-start":    "/v2/instance/{UUID}/action/start",
		"hosting.instance-stop":     "/v2/instance/{UUID}/action/halt",
		"hosting.instance-reboot":   "/v2/instance/{UUID}/action/restart",
		"hosting.region-list":       "/v2/regions",
		"hosting.plan-list":         "/v2/instance-plans",
		"hosting.os-list":           "/v2/operating-systems",
	}

	return endpoints[capName]
}

// invoke creates a Vultr API request and executes it.
func (a *vultrAdapter) invoke(ctx context.Context, method string, body json.RawMessage) (*http.Response, error) {
	token := ""

	req, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+method, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	return resp, nil
}

// handleResponse wraps response bodies with error handling.
func (a *vultrAdapter) handleResponse(resp *http.Response, method string, maxBytes int64) (json.RawMessage, error) {
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode == 200 {
		return json.RawMessage(bodyBytes), nil
	}
	if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 422 {
		return nil, &adapter.InvocationError{MayHaveSideEffects: false, Cause: fmt.Errorf("vultr %s rejected: status %d, body %s", method, resp.StatusCode, string(bodyBytes))}
	}
	if resp.StatusCode >= 500 {
		return nil, &adapter.InvocationError{MayHaveSideEffects: true, Cause: fmt.Errorf("vultr %s failed: status %d, body %s", method, resp.StatusCode, string(bodyBytes))}
	}
	return nil, &adapter.InvocationError{MayHaveSideEffects: true, Cause: fmt.Errorf("vultr %s unknown response: status %d, body %s", method, resp.StatusCode, string(bodyBytes))}
}

// parseVultrResponse extracts the resource from Vultr's envelope.
func (a *vultrAdapter) parseEnvelope(resp json.RawMessage, typeName string) (map[string]any, error) {
	if len(resp) == 0 {
		return nil, fmt.Errorf("empty response for %s", typeName)
	}

	var parsed any
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal Vultr envelope: %w", err)
	}

	mapped, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid Vultr response type for %s: expected object, got %T", typeName, parsed)
	}

	fieldName := strings.ToLower(typeName)
	if val, exists := mapped[fieldName]; exists {
		mappedValue, ok := val.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Vultr response field %s is not an object", fieldName)
		}
		return mappedValue, nil
	}

	if len(resp) > 0 {
		return parsed.(map[string]any), nil
	}

	return nil, fmt.Errorf("field %s not found in Vultr response", fieldName)
}
