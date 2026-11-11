// Command controller builds the controller runtime (state store, adapter
// registry with configured adapters, and workflow engine) and runs the
// management API over HTTPS. The listener is disabled by default and
// currently serves operation inspection.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	keyManager, err := authkey.NewManager(runtime.Store)
	if err != nil {
		return err
	}
	handler, err := managementapi.NewHandler(runtime.Store, keyManager, management.Audience, nil)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", management.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen for management API: %w", err)
	}
	server := &http.Server{
		Addr:              management.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(tls.NewListener(listener, tlsConfig))
	}()
	if stdout != nil {
		if adapters := runtime.AdapterIDs(); len(adapters) > 0 {
			_, _ = fmt.Fprintf(stdout, "Registered adapters: %s\n", strings.Join(adapters, ", "))
		}
		_, _ = fmt.Fprintf(stdout, "Management HTTPS listener ready at %s\n", management.ListenAddress)
	}
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
