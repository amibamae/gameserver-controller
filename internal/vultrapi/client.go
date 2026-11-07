// Package vultrapi contains a small, side-effect-aware client for the Vultr
// Compute Instances API. It does not store credentials or retry mutations.
package vultrapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL      = "https://api.vultr.com/v2"
	maximumResponseSize = 4 << 20
	maximumPages        = 1000
)

var (
	ErrInvalidRequest  = errors.New("invalid Vultr API request")
	ErrProviderReject  = errors.New("Vultr rejected the request")
	ErrRequestFailed   = errors.New("Vultr API request failed")
	ErrOutcomeUnknown  = errors.New("Vultr create outcome is unknown")
	ErrInvalidResponse = errors.New("invalid Vultr API response")
	resourceIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
)

// Client makes authenticated HTTPS calls to the Vultr v2 API. Its HTTP client
// does not follow redirects, which prevents credentials from being forwarded
// to a redirect target and prevents an implicit replay of a create request.
type Client struct {
	baseURL *url.URL
	http    *http.Client
}

// Instance contains the provider fields used by the controller. Additional
// fields returned by Vultr are intentionally ignored for forward compatibility.
type Instance struct {
	ID          string   `json:"id"`
	Region      string   `json:"region"`
	Plan        string   `json:"plan"`
	OSID        int      `json:"os_id"`
	Label       string   `json:"label"`
	Hostname    string   `json:"hostname"`
	Status      string   `json:"status"`
	PowerStatus string   `json:"power_status"`
	MainIP      string   `json:"main_ip"`
	Tags        []string `json:"tags"`
}

type CreateInstanceRequest struct {
	Region    string
	Plan      string
	OSID      int
	Label     string
	Hostname  string
	SSHKeyIDs []string
	UserData  string // Base64-encoded, non-secret cloud-init data.
	Tags      []string
}

type createInstanceBody struct {
	Region    string   `json:"region"`
	Plan      string   `json:"plan"`
	OSID      int      `json:"os_id"`
	Label     string   `json:"label"`
	Hostname  string   `json:"hostname,omitempty"`
	SSHKeyIDs []string `json:"sshkey_id,omitempty"`
	UserData  string   `json:"user_data,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

type instanceEnvelope struct {
	Instance Instance `json:"instance"`
}

type instanceListEnvelope struct {
	Instances []Instance        `json:"instances"`
	Meta      *instanceListMeta `json:"meta"`
}

type instanceListMeta struct {
	Links *instanceListLinks `json:"links"`
}

type instanceListLinks struct {
	Next string `json:"next"`
}

// NewClient creates a client for the production Vultr API. It accepts an
// optional caller-owned HTTP client for transport policy and tests.
func NewClient(httpClient *http.Client) *Client {
	client, _ := NewClientAt(defaultBaseURL, httpClient)
	return client
}

// NewClientAt is intended for transport tests and controlled API-compatible
// endpoints. The URL must be HTTPS and must not contain credentials or a query.
func NewClientAt(baseURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: API base URL must be an HTTPS origin", ErrInvalidRequest)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v2"
	} else if parsed.Path != "/v2" {
		return nil, fmt.Errorf("%w: API base URL path must be /v2", ErrInvalidRequest)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	} else {
		copyClient := *httpClient
		if copyClient.Timeout == 0 {
			copyClient.Timeout = 30 * time.Second
		}
		httpClient = &copyClient
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{baseURL: parsed, http: httpClient}, nil
}

// CreateInstance sends one POST to the create endpoint and never retries it.
// Any transport/5xx/response-parse failure after dispatch is reported as an
// unknown outcome so the caller can reconcile by the unique label or tag.
func (c *Client) CreateInstance(ctx context.Context, token []byte, input CreateInstanceRequest) (Instance, error) {
	if err := validateCreateRequest(input); err != nil {
		return Instance{}, err
	}
	body, err := json.Marshal(createInstanceBody{
		Region: input.Region, Plan: input.Plan, OSID: input.OSID, Label: input.Label,
		Hostname: input.Hostname, SSHKeyIDs: cloneStrings(input.SSHKeyIDs),
		UserData: input.UserData, Tags: cloneStrings(input.Tags),
	})
	if err != nil {
		return Instance{}, fmt.Errorf("%w: could not encode create request", ErrInvalidRequest)
	}
	response, err := c.request(ctx, token, http.MethodPost, "instances", url.Values{}, body, true)
	if err != nil {
		return Instance{}, err
	}
	var envelope instanceEnvelope
	if err := decodeJSON(response, &envelope); err != nil || !validResourceID(envelope.Instance.ID) {
		return Instance{}, ErrOutcomeUnknown
	}
	return envelope.Instance, nil
}

// GetInstance retrieves one exact provider instance ID.
func (c *Client) GetInstance(ctx context.Context, token []byte, instanceID string) (Instance, error) {
	if !validResourceID(instanceID) {
		return Instance{}, fmt.Errorf("%w: invalid instance ID", ErrInvalidRequest)
	}
	response, err := c.request(ctx, token, http.MethodGet, "instances/"+url.PathEscape(instanceID), url.Values{}, nil, false)
	if err != nil {
		return Instance{}, err
	}
	var envelope instanceEnvelope
	if err := decodeJSON(response, &envelope); err != nil || envelope.Instance.ID != instanceID {
		return Instance{}, ErrInvalidResponse
	}
	return envelope.Instance, nil
}

// ListInstancesByLabel returns every instance with the exact provider label.
// A unique controller label lets a caller reconcile a create whose HTTP
// response was lost. Cursor values come from Vultr's response and are sent
// only as query parameters to the same fixed endpoint.
func (c *Client) ListInstancesByLabel(ctx context.Context, token []byte, label string) ([]Instance, error) {
	if !validLabel(label) {
		return nil, fmt.Errorf("%w: invalid instance label", ErrInvalidRequest)
	}
	var instances []Instance
	seenCursors := make(map[string]bool)
	cursor := ""
	for page := 0; page < maximumPages; page++ {
		query := url.Values{"label": {label}, "per_page": {"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		response, err := c.request(ctx, token, http.MethodGet, "instances", query, nil, false)
		if err != nil {
			return nil, err
		}
		var envelope instanceListEnvelope
		if err := decodeJSON(response, &envelope); err != nil {
			return nil, ErrInvalidResponse
		}
		if envelope.Meta == nil || envelope.Meta.Links == nil {
			return nil, ErrInvalidResponse
		}
		for _, instance := range envelope.Instances {
			if instance.Label == label {
				if !validResourceID(instance.ID) {
					return nil, ErrInvalidResponse
				}
				instances = append(instances, instance)
			}
		}
		cursor = envelope.Meta.Links.Next
		if cursor == "" {
			return instances, nil
		}
		if len(cursor) > 2048 || seenCursors[cursor] {
			return nil, ErrInvalidResponse
		}
		seenCursors[cursor] = true
	}
	return nil, ErrInvalidResponse
}

func (c *Client) request(ctx context.Context, token []byte, method, path string, query url.Values, body []byte, mutation bool) ([]byte, error) {
	if c == nil || c.baseURL == nil || c.http == nil || ctx == nil || ctx.Err() != nil || !validToken(token) {
		return nil, fmt.Errorf("%w: context, client, and API credential are required", ErrInvalidRequest)
	}
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/" + path
	endpoint.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("%w: could not construct request", ErrInvalidRequest)
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		if mutation {
			return nil, ErrOutcomeUnknown
		}
		return nil, ErrRequestFailed
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode >= 400 && response.StatusCode < 500 {
			return nil, fmt.Errorf("%w: HTTP %d", ErrProviderReject, response.StatusCode)
		}
		if mutation {
			return nil, ErrOutcomeUnknown
		}
		return nil, fmt.Errorf("%w: HTTP %d", ErrRequestFailed, response.StatusCode)
	}
	value, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseSize+1))
	if err != nil || len(value) > maximumResponseSize {
		if mutation {
			return nil, ErrOutcomeUnknown
		}
		return nil, ErrInvalidResponse
	}
	return value, nil
}

func validateCreateRequest(input CreateInstanceRequest) error {
	if !validShortField(input.Region, 200) || !validShortField(input.Plan, 200) || input.OSID <= 0 ||
		!validLabel(input.Label) || (input.Hostname != "" && !validHostname(input.Hostname)) {
		return fmt.Errorf("%w: region, plan, os_id, label, or hostname is invalid", ErrInvalidRequest)
	}
	if len(input.SSHKeyIDs) > 100 {
		return fmt.Errorf("%w: too many SSH key IDs", ErrInvalidRequest)
	}
	for _, id := range input.SSHKeyIDs {
		if !validResourceID(id) {
			return fmt.Errorf("%w: invalid SSH key ID", ErrInvalidRequest)
		}
	}
	if input.UserData != "" {
		decoded, err := base64.StdEncoding.DecodeString(input.UserData)
		if err != nil || len(decoded) > 64<<10 {
			return fmt.Errorf("%w: user_data must be base64-encoded and at most 64 KiB", ErrInvalidRequest)
		}
	}
	if len(input.Tags) > 100 {
		return fmt.Errorf("%w: too many tags", ErrInvalidRequest)
	}
	seen := make(map[string]bool, len(input.Tags))
	for _, tag := range input.Tags {
		if !validShortField(tag, 200) || seen[tag] {
			return fmt.Errorf("%w: invalid or duplicate tag", ErrInvalidRequest)
		}
		seen[tag] = true
	}
	return nil
}

func decodeJSON(value []byte, destination any) error {
	if len(value) == 0 || len(value) > maximumResponseSize {
		return ErrInvalidResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	if err := decoder.Decode(destination); err != nil {
		return ErrInvalidResponse
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidResponse
	}
	return nil
}

func validToken(value []byte) bool {
	if len(value) == 0 || len(value) > 4096 || !bytes.Equal(bytes.TrimSpace(value), value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func validResourceID(value string) bool { return resourceIDPattern.MatchString(value) }

func validShortField(value string, maximum int) bool {
	if strings.TrimSpace(value) == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validLabel(value string) bool {
	return validShortField(value, 200)
}

func validHostname(value string) bool {
	if len(value) > 253 || strings.TrimSpace(value) != value || strings.ContainsAny(value, " /\\\t\r\n") {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string(nil), values...)
}

// ParseOSID converts the protocol's provider-neutral string to the integer ID
// required by Vultr's create-instance request body.
func ParseOSID(value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%w: provider OS ID must be a positive integer", ErrInvalidRequest)
	}
	return parsed, nil
}
