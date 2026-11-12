package notification

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
)

// SMTP TLS modes. Plain-text SMTP is not offered.
const (
	TLSStartTLS = "starttls"
	TLSImplicit = "implicit"
)

// SMTP sends events as plain-text email through an authenticated SMTP
// submission server. The password is the secret.
type SMTP struct {
	Host     string
	Port     int
	TLSMode  string
	Username string
	From     string
	To       []string
	Timeout  time.Duration
	// TLSRoots overrides the system certificate roots; tests only.
	TLSRoots *x509.CertPool
}

func (s SMTP) Kind() string        { return "email" }
func (s SMTP) DisplayName() string { return "SMTP email notifier" }

// ValidAddress accepts one bare email address with no display name or line
// breaks, so it cannot inject headers.
func ValidAddress(value string) bool {
	if strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value
}

// Message renders the email. Header values come from event fields that the
// schema limits; line breaks are removed anyway.
func (s SMTP) Message(event Event, now time.Time) []byte {
	clean := func(value string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(value) }
	subject := fmt.Sprintf("[gameserver-controller] %s: %s %s", event.Severity, event.EventType, event.AdapterID)
	var b strings.Builder
	header := func(name, value string) { b.WriteString(name + ": " + clean(value) + "\r\n") }
	header("From", s.From)
	header("To", strings.Join(s.To, ", "))
	header("Subject", subject)
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+event.EventID+"@gameserver-controller.invalid>")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	lines := []string{
		event.Summary, "",
		"Event: " + event.EventType,
		"Severity: " + event.Severity,
		"Environment: " + event.EnvironmentID,
		"Adapter: " + event.AdapterID,
		"Proxy instance: " + event.Subject.ExternalID,
		"Outage started: " + event.OutageStartedAt,
		"Occurred: " + event.OccurredAt,
		"Event ID: " + event.EventID,
	}
	for _, line := range lines {
		b.WriteString(clean(line) + "\r\n")
	}
	return []byte(b.String())
}

func (s SMTP) Send(ctx context.Context, event Event, secret []byte) string {
	if !ValidAddress(s.From) || len(s.To) == 0 || s.Host == "" || s.Port < 1 || s.Port > 65535 {
		return integrationmonitor.Rejected
	}
	for _, to := range s.To {
		if !ValidAddress(to) {
			return integrationmonitor.Rejected
		}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	err := s.deliver(ctx, deadline, event, string(secret))
	switch {
	case err == nil:
		return integrationmonitor.Delivered
	case errors.Is(err, errNotSent):
		return integrationmonitor.Rejected
	default:
		var protocolErr *textproto.Error
		// A 5xx reply is a permanent refusal; anything else may be retried.
		if errors.As(err, &protocolErr) && protocolErr.Code >= 500 {
			return integrationmonitor.Rejected
		}
		return integrationmonitor.Unknown
	}
}

// errNotSent marks permanent failures before the message is sent, such as a
// server that does not offer STARTTLS. Connection failures are not wrapped:
// nothing was sent, so they are worth retrying.
var errNotSent = errors.New("message not sent")

func (s SMTP) deliver(ctx context.Context, deadline time.Time, event Event, password string) error {
	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	tlsConfig.RootCAs = s.TLSRoots
	dialer := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	var err error
	if s.TLSMode == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return errors.New("connect failed")
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return errors.New("greeting failed")
	}
	defer client.Close()
	if s.TLSMode != TLSImplicit {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("%w: server does not offer STARTTLS", errNotSent)
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return errors.New("STARTTLS failed")
		}
	}
	// A 5xx reply (wrong credentials) is classified as rejected by Send.
	if err := client.Auth(smtp.PlainAuth("", s.Username, password, s.Host)); err != nil {
		return err
	}
	if err := client.Mail(s.From); err != nil {
		return err
	}
	for _, to := range s.To {
		if err := client.Rcpt(to); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(s.Message(event, time.Now())); err != nil {
		return errors.New("write failed")
	}
	// The server's reply to the end of DATA decides delivery.
	if err := writer.Close(); err != nil {
		return err
	}
	_ = client.Quit()
	return nil
}
