package notification

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
)

var ntfyTopicPattern = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)

// Ntfy publishes events to an ntfy topic with a bearer access token.
type Ntfy struct {
	ServerURL string
	Topic     string
	Client    *http.Client
}

func (n Ntfy) Kind() string        { return "push" }
func (n Ntfy) DisplayName() string { return "ntfy push notifier" }

// ValidNtfyServer accepts https, or http to a loopback address, with no path
// beyond "/", query, fragment, or user information.
func ValidNtfyServer(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname()))
}

// ValidNtfyTopic matches ntfy's topic name rule.
func ValidNtfyTopic(topic string) bool { return ntfyTopicPattern.MatchString(topic) }

// Priority maps severity to ntfy's 1-5 scale.
func Priority(severity string) string {
	switch severity {
	case "critical":
		return "5"
	case "warning":
		return "4"
	}
	return "3"
}

func (n Ntfy) Send(ctx context.Context, event Event, secret []byte) string {
	token := strings.TrimSpace(string(secret))
	if !ValidNtfyServer(n.ServerURL) || !ValidNtfyTopic(n.Topic) || token == "" || strings.ContainsAny(token, "\r\n") {
		return integrationmonitor.Rejected
	}
	target := strings.TrimSuffix(n.ServerURL, "/") + "/" + n.Topic
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(event.Summary))
	if err != nil {
		return integrationmonitor.Rejected
	}
	title := "Proxy recovered"
	tags := "white_check_mark"
	if event.Severity == "critical" {
		title, tags = "Proxy outage", "rotating_light"
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Title", title)
	request.Header.Set("Priority", Priority(event.Severity))
	request.Header.Set("Tags", tags)
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	return postOutcome(n.Client, request)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
