package notification

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

func testEvent() Event {
	tracker := integrationmonitor.NewTracker(integrationmonitor.Policy{CheckInterval: time.Minute, FailureThreshold: 1, InitialBackoff: time.Second, MaxBackoff: time.Second},
		integrationmonitor.Subject{AdapterID: "integration.velocity", EnvironmentID: "production", ResourceKind: "proxy-instance", ExternalID: "velocity-edge-001"})
	events, _ := tracker.Observe(integrationmonitor.Unreachable, time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC))
	return events[0]
}

func TestWebhookFormatsAndOutcomes(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	var headers []http.Header
	status := http.StatusNoContent
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies, headers = append(bodies, body), append(headers, r.Header.Clone())
		code := status
		mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://elsewhere.example/hook", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(code)
	}))
	defer server.Close()
	event := testEvent()
	for _, format := range []string{FormatDiscord, FormatSlack, FormatJSON} {
		hook := Webhook{Format: format, Client: server.Client()}
		if got := hook.Send(context.Background(), event, []byte(server.URL+"/hook\n")); got != integrationmonitor.Delivered {
			t.Fatalf("%s: outcome %s", format, got)
		}
	}
	if bodies[0]["content"] != event.Summary || bodies[0]["allowed_mentions"] == nil {
		t.Errorf("discord body = %v", bodies[0])
	}
	if bodies[1]["text"] != event.Summary {
		t.Errorf("slack body = %v", bodies[1])
	}
	if bodies[2]["event_id"] != event.EventID || headers[2].Get("Idempotency-Key") != event.EventID {
		t.Errorf("json body = %v", bodies[2])
	}
	for code, want := range map[int]string{
		http.StatusBadRequest: integrationmonitor.Rejected, http.StatusNotFound: integrationmonitor.Rejected,
		http.StatusTooManyRequests: integrationmonitor.Unknown, http.StatusBadGateway: integrationmonitor.Unknown,
	} {
		mu.Lock()
		status = code
		mu.Unlock()
		if got := (Webhook{Format: FormatSlack, Client: server.Client()}).Send(context.Background(), event, []byte(server.URL+"/hook")); got != want {
			t.Errorf("status %d: outcome %s, want %s", code, got, want)
		}
	}
	hook := Webhook{Format: FormatSlack, Client: server.Client()}
	if got := hook.Send(context.Background(), event, []byte(server.URL+"/redirect")); got != integrationmonitor.Rejected {
		t.Errorf("redirect outcome %s; a secret URL must not be followed elsewhere", got)
	}
	if got := hook.Send(context.Background(), event, []byte("http://example.test/hook")); got != integrationmonitor.Rejected {
		t.Errorf("plain http webhook outcome %s", got)
	}
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	closed.Close()
	if got := (Webhook{Format: FormatSlack, Client: closed.Client()}).Send(context.Background(), event, []byte(closed.URL)); got != integrationmonitor.Unknown {
		t.Errorf("unreachable endpoint outcome %s", got)
	}
}

func TestNtfyRequest(t *testing.T) {
	var got *http.Request
	var body string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		got, body = r.Clone(context.Background()), string(data)
	}))
	defer server.Close()
	event := testEvent()
	sender := Ntfy{ServerURL: server.URL, Topic: "gsc-alerts", Client: server.Client()}
	if outcome := sender.Send(context.Background(), event, []byte("tk_secret\n")); outcome != integrationmonitor.Delivered {
		t.Fatalf("outcome %s", outcome)
	}
	if got.URL.Path != "/gsc-alerts" || got.Header.Get("Authorization") != "Bearer tk_secret" || got.Header.Get("Priority") != "5" || body != event.Summary {
		t.Errorf("request = %s %v %q", got.URL.Path, got.Header, body)
	}
	if outcome := (Ntfy{ServerURL: server.URL, Topic: "bad topic", Client: server.Client()}).Send(context.Background(), event, []byte("tk")); outcome != integrationmonitor.Rejected {
		t.Errorf("bad topic outcome %s", outcome)
	}
	for value, want := range map[string]bool{"https://ntfy.sh": true, "http://127.0.0.1:8080": true, "http://ntfy.example": false, "https://ntfy.sh/topic": false} {
		if ValidNtfyServer(value) != want {
			t.Errorf("ValidNtfyServer(%q) != %v", value, want)
		}
	}
}

// fakeSMTP is a minimal submission server for one connection at a time.
type fakeSMTP struct {
	listener net.Listener
	tls      *tls.Config
	implicit bool
	authCode string
	mu       sync.Mutex
	messages []string
	rcpts    []string
}

func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func startSMTP(t *testing.T, implicit bool, authCode string) (*fakeSMTP, *x509.CertPool) {
	t.Helper()
	config, pool := selfSigned(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicit {
		listener = tls.NewListener(listener, config)
	}
	server := &fakeSMTP{listener: listener, tls: config, implicit: implicit, authCode: authCode}
	go server.serve()
	t.Cleanup(func() { listener.Close() })
	return server, pool
}

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.handle(conn)
	}
}

func (s *fakeSMTP) handle(conn net.Conn) {
	defer conn.Close()
	reader, writer := bufio.NewReader(conn), bufio.NewWriter(conn)
	reply := func(line string) { writer.WriteString(line + "\r\n"); writer.Flush() }
	secure := s.implicit
	reply("220 fake ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"):
			if secure {
				reply("250-fake\r\n250 AUTH PLAIN")
			} else {
				reply("250-fake\r\n250 STARTTLS")
			}
		case command == "STARTTLS":
			reply("220 go ahead")
			tlsConn := tls.Server(conn, s.tls)
			if tlsConn.Handshake() != nil {
				return
			}
			conn, secure = tlsConn, true
			reader, writer = bufio.NewReader(conn), bufio.NewWriter(conn)
		case strings.HasPrefix(command, "AUTH"):
			reply(s.authCode)
		case strings.HasPrefix(command, "MAIL FROM"):
			reply("250 ok")
		case strings.HasPrefix(command, "RCPT TO"):
			s.mu.Lock()
			s.rcpts = append(s.rcpts, strings.TrimSpace(line))
			s.mu.Unlock()
			reply("250 ok")
		case command == "DATA":
			reply("354 send")
			var message strings.Builder
			for {
				data, err := reader.ReadString('\n')
				if err != nil || data == ".\r\n" {
					break
				}
				message.WriteString(data)
			}
			s.mu.Lock()
			s.messages = append(s.messages, message.String())
			s.mu.Unlock()
			reply("250 queued")
		case command == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func TestSMTPDeliversOverBothTLSModes(t *testing.T) {
	event := testEvent()
	for _, mode := range []string{TLSImplicit, TLSStartTLS} {
		t.Run(mode, func(t *testing.T) {
			server, pool := startSMTP(t, mode == TLSImplicit, "235 ok")
			port := server.listener.Addr().(*net.TCPAddr).Port
			sender := SMTP{Host: "127.0.0.1", Port: port, TLSMode: mode, Username: "alerts", From: "gsc@example.test",
				To: []string{"ops@example.test", "oncall@example.test"}, Timeout: 5 * time.Second, TLSRoots: pool}
			if got := sender.Send(context.Background(), event, []byte("app-password")); got != integrationmonitor.Delivered {
				t.Fatalf("outcome %s", got)
			}
			server.mu.Lock()
			defer server.mu.Unlock()
			if len(server.messages) != 1 || len(server.rcpts) != 2 {
				t.Fatalf("messages %d, recipients %d", len(server.messages), len(server.rcpts))
			}
			message := server.messages[0]
			if !strings.Contains(message, "Message-ID: <"+event.EventID+"@") || !strings.Contains(message, event.Summary) || strings.Contains(message, "app-password") {
				t.Errorf("message = %q", message)
			}
		})
	}
}

func TestSMTPOutcomes(t *testing.T) {
	event := testEvent()
	server, pool := startSMTP(t, true, "535 bad credentials")
	port := server.listener.Addr().(*net.TCPAddr).Port
	sender := SMTP{Host: "127.0.0.1", Port: port, TLSMode: TLSImplicit, Username: "alerts", From: "gsc@example.test", To: []string{"ops@example.test"}, Timeout: 5 * time.Second, TLSRoots: pool}
	if got := sender.Send(context.Background(), event, []byte("wrong")); got != integrationmonitor.Rejected {
		t.Errorf("bad credentials outcome %s", got)
	}
	server.listener.Close()
	if got := sender.Send(context.Background(), event, []byte("pw")); got != integrationmonitor.Unknown {
		t.Errorf("server down outcome %s, want a retry", got)
	}
	injected := sender
	injected.To = []string{"ops@example.test\r\nBcc: attacker@example.test"}
	if got := injected.Send(context.Background(), event, []byte("pw")); got != integrationmonitor.Rejected {
		t.Errorf("header injection outcome %s", got)
	}
	for value, want := range map[string]bool{"ops@example.test": true, "Ops <ops@example.test>": false, "not an address": false} {
		if ValidAddress(value) != want {
			t.Errorf("ValidAddress(%q) != %v", value, want)
		}
	}
}

type credentials map[string]string

func (c credentials) WithCredential(reference string, use func([]byte) error) error {
	value, ok := c[reference]
	if !ok {
		return io.ErrUnexpectedEOF
	}
	return use([]byte(value))
}

type recordingSender struct {
	mu     sync.Mutex
	secret string
}

func (r *recordingSender) got() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.secret
}

func (r *recordingSender) Kind() string        { return "webhook" }
func (r *recordingSender) DisplayName() string { return "recording" }
func (r *recordingSender) Send(_ context.Context, _ Event, secret []byte) string {
	r.mu.Lock()
	r.secret = string(secret)
	r.mu.Unlock()
	return integrationmonitor.Delivered
}

func TestAdapterThroughTheRegistry(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{}
	notifier, err := New(Config{AdapterID: "integration.notify-discord", EnvironmentID: "production", SecretRef: "systemd-credential://discord-webhook",
		Sender: sender, Credentials: credentials{"systemd-credential://discord-webhook": "https://discord.example/api/webhooks/1/x"}}, schemas.ProtocolVersion())
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(notifier); err != nil {
		t.Fatalf("descriptor rejected: %v", err)
	}
	event := testEvent()
	ref := []adapter.SecretReference{{SecretRef: "systemd-credential://discord-webhook", Purpose: "webhook"}}
	destination := integrationmonitor.Destination{AdapterID: "integration.notify-discord", EnvironmentID: "production", SecretRefs: ref}
	dispatcher := integrationmonitor.NewDispatcher(registry, []integrationmonitor.Destination{destination}, func(string, map[string]any) {})
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher.Start(ctx)
	dispatcher.Publish(event)
	deadline := time.Now().Add(5 * time.Second)
	for sender.got() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	dispatcher.Wait()
	if sender.got() != "https://discord.example/api/webhooks/1/x" {
		t.Fatalf("sender got secret %q", sender.got())
	}

	// Without the authorizing secret reference the adapter refuses to act.
	unauthorized := adapter.InvokeRequest{JSONRPC: "2.0", ID: "r1", Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: schemas.ProtocolVersion(), Capability: integrationmonitor.SendCapability,
		RequestContext: adapter.RequestContext{RequestID: "r1", CorrelationID: "c", EnvironmentID: "production", Deadline: time.Now().Add(time.Minute)},
		Input:          json.RawMessage(`{"event":` + mustJSON(event) + `}`),
	}}
	if _, err := registry.Invoke(context.Background(), "integration.notify-discord", unauthorized); err == nil {
		t.Fatal("a call without the secret reference was accepted")
	}
	// An event with a player field never reaches the adapter.
	withPlayer := strings.Replace(mustJSON(event), `"summary"`, `"player_name":"synthetic-player-name-canary","summary"`, 1)
	unauthorized.Params.Input = json.RawMessage(`{"event":` + withPlayer + `}`)
	unauthorized.Params.RequestContext.SecretRefs = ref
	if _, err := registry.Invoke(context.Background(), "integration.notify-discord", unauthorized); err == nil || !strings.Contains(err.Error(), "invalid input") {
		t.Fatalf("event with a player field: %v", err)
	}
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
