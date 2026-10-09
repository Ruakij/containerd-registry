// Command containerd-registry serves the OCI distribution API from the
// containerd of its node.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"syscall"
	"time"

	"github.com/Ruakij/containerd-registry/internal/config"
	"github.com/Ruakij/containerd-registry/internal/route"
	"github.com/Ruakij/containerd-registry/internal/server"
	"github.com/Ruakij/containerd-registry/internal/store"
)

var (
	// Set by the build process
	version = "dev"
)

func main() {
	configPath := flag.String("config", "/etc/containerd-registry/config.yml", "path to the config file")
	showVersion := flag.Bool("version", false, "show version")
	flag.Parse()

	if *showVersion {
		fmt.Println(path.Base(os.Args[0]), version)
		return
	}
	if err := run(*configPath); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, cfg.Storage.Containerd.Address)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	srv := &http.Server{
		Addr: net.JoinHostPort(cfg.HTTP.Address, cfg.HTTP.Port),
		Handler: server.New(st, router{route.New(cfg.Proxy)}, log, server.Errors{
			NotFound:        store.ErrNotFound,
			UnknownUpstream: route.ErrUnknownUpstream,
			InvalidName:     route.ErrInvalidName,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("serving", "address", srv.Addr, "version", version)
		if cfg.HTTP.TLS != nil {
			errc <- srv.ListenAndServeTLS(cfg.HTTP.TLS.Cert, cfg.HTTP.TLS.Key)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// router adapts route.Router to the fully qualified name the server works with.
type router struct{ *route.Router }

func (r router) Resolve(name, ns string) (string, error) {
	repo, err := r.Router.Resolve(name, ns)
	return repo.String(), err
}
