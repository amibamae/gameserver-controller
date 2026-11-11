package cloudflaredns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	defaultAPIBase   = "https://api.cloudflare.com/client/v4"
	maxResponseBytes = 1 << 20
	recordComment    = "managed by gameserver-controller"
)

var (
	// errRefused means Cloudflare answered with a 4xx status, so the request
	// changed nothing.
	errRefused = errors.New("Cloudflare refused the request")
	// errUncertain covers transport errors, 5xx replies, and malformed
	// replies: a mutating request may or may not have been applied.
	errUncertain = errors.New("Cloudflare request outcome is unknown")

	recordIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// dnsRecord is one Cloudflare DNS record. A protocol record with several
// values maps to several Cloudflare records with the same name and type.
type dnsRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment,omitempty"`
}

type apiEnvelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		TotalCount int `json:"total_count"`
	} `json:"result_info"`
}

// client calls the Cloudflare API v4 for one zone. Errors never include the
// token, URLs, or response bodies.
type client struct {
	base   string
	zoneID string
	http   *http.Client
}

func (c *client) records(ctx context.Context, token []byte, name, recordType string) ([]dnsRecord, error) {
	query := url.Values{"name": {name}, "type": {recordType}, "per_page": {"100"}}
	var envelope apiEnvelope
	if err := c.do(ctx, token, http.MethodGet, "/dns_records?"+query.Encode(), nil, &envelope); err != nil {
		return nil, err
	}
	var records []dnsRecord
	if err := json.Unmarshal(envelope.Result, &records); err != nil {
		return nil, errUncertain
	}
	if envelope.ResultInfo != nil && envelope.ResultInfo.TotalCount > len(records) {
		return nil, errUncertain
	}
	for _, record := range records {
		if !recordIDPattern.MatchString(record.ID) || !strings.EqualFold(record.Name, name) || record.Type != recordType {
			return nil, errUncertain
		}
	}
	return records, nil
}

func (c *client) create(ctx context.Context, token []byte, record dnsRecord) error {
	record.ID = ""
	record.Comment = recordComment
	return c.do(ctx, token, http.MethodPost, "/dns_records", record, &apiEnvelope{})
}

func (c *client) patch(ctx context.Context, token []byte, id string, ttl int) error {
	if !recordIDPattern.MatchString(id) {
		return errRefused
	}
	return c.do(ctx, token, http.MethodPatch, "/dns_records/"+id, map[string]any{"ttl": ttl, "proxied": false}, &apiEnvelope{})
}

func (c *client) delete(ctx context.Context, token []byte, id string) error {
	if !recordIDPattern.MatchString(id) {
		return errRefused
	}
	return c.do(ctx, token, http.MethodDelete, "/dns_records/"+id, nil, &apiEnvelope{})
}

func (c *client) do(ctx context.Context, token []byte, method, path string, body any, envelope *apiEnvelope) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errRefused
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+"/zones/"+c.zoneID+path, payload)
	if err != nil {
		return errRefused
	}
	value := bytes.TrimSpace(token)
	if len(value) == 0 || bytes.ContainsAny(value, "\r\n\t ") {
		return errRefused
	}
	request.Header.Set("Authorization", "Bearer "+string(value))
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return errUncertain
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return errUncertain
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		return errRefused
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errUncertain
	}
	if err := json.Unmarshal(data, envelope); err != nil || !envelope.Success {
		return errUncertain
	}
	return nil
}
