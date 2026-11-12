package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
)

// Webhook formats.
const (
	FormatDiscord = "discord"
	FormatSlack   = "slack"
	FormatJSON    = "json"
)

// Webhook posts events to a Discord or Slack incoming webhook, or as the raw
// notification-event JSON to any HTTPS endpoint. The webhook URL is the
// secret: it is read from a systemd credential and never logged.
type Webhook struct {
	Format string
	Client *http.Client
}

func (w Webhook) Kind() string { return "webhook" }

func (w Webhook) DisplayName() string {
	switch w.Format {
	case FormatDiscord:
		return "Discord webhook notifier"
	case FormatSlack:
		return "Slack webhook notifier"
	}
	return "JSON webhook notifier"
}

// ValidWebhookURL accepts only an absolute https URL without user information
// or a fragment.
func ValidWebhookURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == ""
}

// Body renders the request body for a format.
func (w Webhook) Body(event Event) ([]byte, error) {
	switch w.Format {
	case FormatDiscord:
		// allowed_mentions with no parse types stops @everyone and similar.
		return json.Marshal(map[string]any{"content": event.Summary, "allowed_mentions": map[string]any{"parse": []string{}}})
	case FormatSlack:
		return json.Marshal(map[string]any{"text": event.Summary})
	}
	return json.Marshal(event)
}

func (w Webhook) Send(ctx context.Context, event Event, secret []byte) string {
	target := strings.TrimSpace(string(secret))
	if !ValidWebhookURL(target) {
		return integrationmonitor.Rejected
	}
	body, err := w.Body(event)
	if err != nil {
		return integrationmonitor.Rejected
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return integrationmonitor.Rejected
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", event.EventID)
	return postOutcome(w.Client, request)
}

// postOutcome sends a request and classifies the response. Transport errors
// are unknown: the request may have arrived. Redirects are not followed, so
// a secret URL is never sent to another host.
func postOutcome(client *http.Client, request *http.Request) string {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(request)
	if err != nil {
		return integrationmonitor.Unknown
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return integrationmonitor.Rejected
	}
	return classifyHTTP(response.StatusCode)
}
