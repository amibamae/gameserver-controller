// Command controller builds the controller runtime (state store, adapter
// registry with configured adapters, and workflow engine) and runs the
// management API over HTTPS. The listener is disabled by default and
// currently serves operation inspection.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/controllerruntime"
	"github.com/amibamae/gameserver-controller/internal/managementapi"
)

const buildIdentity = "gameserver-controller-runtime-v1"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && args[0] == "version" {
		_, err := fmt.Fprintln(stdout, buildIdentity)
		return err
	}
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(stderr, "usage: controller serve --config PATH")
		return errors.New("unsupported command")
	}
	flags := flag.NewFlagSet("controller serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "validated controller YAML configuration")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*configPath) == "" {
		return errors.New("serve requires --config PATH and accepts no positional arguments")
	}
	value, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !value.Controller.ManagementAPI.Enabled {
		return errors.New("management API listener is disabled in configuration")
	}
	return serve(ctx, value, *configPath, stdout)
}

func serve(ctx context.Context, value config.Config, configPath string, stdout io.Writer) error {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("controller context is unavailable")
	}
	management := value.Controller.ManagementAPI
	tlsConfig, err := managementapi.LoadTLSConfig(management.TLSCertificate, management.TLSPrivateKey)
	if err != nil {
		return err
	}
	runtime, err := controllerruntime.Build(ctx, value, configPath, controllerruntime.Options{
		CredentialDirectory: os.Getenv("CREDENTIALS_DIRECTORY"),
		ControllerVersion:   buildIdentity,
	})
	if err != nil {
		return err
	}
	defer runtime.Close()
	handler, err := managementHandler(runtime, management)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", management.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for management API: %w", err)
	}
	server := newManagementServer(management.ListenAddress, handler)
	// Start background work before serving: handlers read the runner to
	// queue accepted operations, so it must exist before the first request.
	runtime.StartMonitoring(ctx, controllerLog(stdout))
	runtime.StartRunner(ctx, controllerLog(stdout))
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(tls.NewListener(listener, tlsConfig))
	}()
	reportStartup(stdout, runtime, management.ListenAddress)
	return awaitServerExit(ctx, server, serveResult)
}

// managementHandler builds the management API over the runtime's store,
// authenticating requests with the store's API keys.
func managementHandler(runtime *controllerruntime.Runtime, management config.ManagementAPI) (http.Handler, error) {
	keyManager, err := authkey.NewManager(runtime.Store)
	if err != nil {
		return nil, err
	}
	return managementapi.NewAPIHandler(managementapi.Config{Store: runtime.Store, Controller: runtime, Authenticator: keyManager,
		Audience: management.Audience, ProtocolVersion: runtime.Schemas.ProtocolVersion()})
}

// newManagementServer bounds every phase of a request so a slow or idle
// client cannot hold a connection open indefinitely.
func newManagementServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// startupInventory is what the startup report lists from the runtime.
type startupInventory interface {
	AdapterIDs() []string
	MonitoredIntegrations() []string
	Notifiers() []string
}

// reportStartup lists the registered adapters and monitored proxies, when
// there are any, and then announces the listener.
func reportStartup(stdout io.Writer, inventory startupInventory, address string) {
	if stdout == nil {
		return
	}
	if adapters := inventory.AdapterIDs(); len(adapters) > 0 {
		_, _ = fmt.Fprintf(stdout, "Registered adapters: %s\n", strings.Join(adapters, ", "))
	}
	if monitored := inventory.MonitoredIntegrations(); len(monitored) > 0 {
		_, _ = fmt.Fprintf(stdout, "Monitoring proxies: %s; notifiers: %s\n", strings.Join(monitored, ", "), strings.Join(inventory.Notifiers(), ", "))
	}
	_, _ = fmt.Fprintf(stdout, "Management HTTPS listener ready at %s\n", address)
}

// awaitServerExit returns when the server stops by itself or, after ctx
// ends, once a graceful shutdown of up to ten seconds completes. A server
// that does not shut down in time is closed.
func awaitServerExit(ctx context.Context, server *http.Server, serveResult <-chan error) error {
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve management API: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			_ = server.Close()
			return fmt.Errorf("shut down management API: %w", err)
		}
		err := <-serveResult
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve management API: %w", err)
		}
		return nil
	}
}

// controllerLog writes monitor events as one JSON object per line, which
// journald keeps with the controller's other output. Fields hold only event
// data and delivery status, never credentials or player data. "time" and
// "message" are reserved: they are set after the caller's fields are copied,
// so a caller field with either name is ignored.
func controllerLog(out io.Writer) func(string, map[string]any) {
	var mu sync.Mutex
	return func(message string, fields map[string]any) {
		if out == nil {
			return
		}
		line := make(map[string]any, len(fields)+2)
		for name, value := range fields {
			line[name] = value
		}
		line["time"] = time.Now().UTC().Format(time.RFC3339)
		line["message"] = message
		encoded, err := json.Marshal(line)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = out.Write(append(encoded, '\n'))
	}
}
