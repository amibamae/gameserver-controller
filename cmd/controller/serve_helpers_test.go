package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type fakeInventory struct{ adapters, monitored, notifiers []string }

func (f fakeInventory) AdapterIDs() []string            { return f.adapters }
func (f fakeInventory) MonitoredIntegrations() []string { return f.monitored }
func (f fakeInventory) Notifiers() []string             { return f.notifiers }

func TestReportStartup(t *testing.T) {
	tests := []struct {
		name      string
		inventory fakeInventory
		want      string
	}{
		{"nothing registered", fakeInventory{}, "Management HTTPS listener ready at 127.0.0.1:8443\n"},
		{"adapters only", fakeInventory{adapters: []string{"game.paper", "hosting.vultr"}},
			"Registered adapters: game.paper, hosting.vultr\nManagement HTTPS listener ready at 127.0.0.1:8443\n"},
		{"monitored proxies", fakeInventory{adapters: []string{"proxy.velocity"}, monitored: []string{"proxy.velocity"}, notifiers: []string{"notify.ntfy", "notify.email"}},
			"Registered adapters: proxy.velocity\nMonitoring proxies: proxy.velocity; notifiers: notify.ntfy, notify.email\nManagement HTTPS listener ready at 127.0.0.1:8443\n"},
		{"monitored without notifiers", fakeInventory{monitored: []string{"proxy.velocity"}},
			"Monitoring proxies: proxy.velocity; notifiers: \nManagement HTTPS listener ready at 127.0.0.1:8443\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			reportStartup(&out, test.inventory, "127.0.0.1:8443")
			if out.String() != test.want {
				t.Fatalf("report = %q, want %q", out.String(), test.want)
			}
		})
	}
	reportStartup(nil, fakeInventory{adapters: []string{"game.paper"}}, "127.0.0.1:8443")
}

func TestNewManagementServerBoundsEveryRequestPhase(t *testing.T) {
	handler := http.NotFoundHandler()
	server := newManagementServer("127.0.0.1:8443", handler)
	if server.Addr != "127.0.0.1:8443" || server.Handler == nil || server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 30*time.Second ||
		server.WriteTimeout != 30*time.Second || server.IdleTimeout != 120*time.Second || server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("server limits = %+v", server)
	}
}

func TestAwaitServerExit(t *testing.T) {
	failure := errors.New("accept failed")
	running, stop := context.WithCancel(context.Background())
	defer stop()
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name   string
		ctx    context.Context
		result error
		want   error
		prefix string
	}{
		{"server closed by itself", running, http.ErrServerClosed, nil, ""},
		{"server failed by itself", running, failure, failure, "serve management API: "},
		{"shutdown after the context ended", ended, http.ErrServerClosed, nil, ""},
		{"server error reported after shutdown", ended, failure, failure, "serve management API: "},
		{"clean return after shutdown", ended, nil, nil, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serveResult := make(chan error, 1)
			if test.ctx == running {
				serveResult <- test.result
			}
			server := &http.Server{}
			if test.ctx == ended {
				// Shutdown of a server that never served returns at once; the
				// serve goroutine then reports its result.
				go func() { serveResult <- test.result }()
			}
			err := awaitServerExit(test.ctx, server, serveResult)
			if test.want == nil {
				if err != nil {
					t.Fatalf("awaitServerExit = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.want) || err.Error() != test.prefix+test.want.Error() {
				t.Fatalf("awaitServerExit = %v, want %q", err, test.prefix+test.want.Error())
			}
		})
	}
}
