package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/engine"
	"github.com/thatsnotmynameio/crew-actions/internal/store/logs"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect"
)

// databaseURLEnv names the variable that holds the engine's database URL.
const databaseURLEnv = "CREW_ACTIONS_DATABASE_URL"

const (
	// readHeaderTimeout bounds how long a client takes to send a request's
	// headers. The server sets no write timeout, which would cut Follow's
	// stream and Next's wait.
	readHeaderTimeout = 10 * time.Second
	// shutdownTimeout is how long a stopping engine waits for the calls in
	// flight.
	shutdownTimeout = 10 * time.Second
)

// engineCommand runs `crew-actions engine`: it listens on loopback, opens
// and migrates the database CREW_ACTIONS_DATABASE_URL names, and serves the
// runner protocol and the client API until ctx ends.
func engineCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("engine", stderr)
	addr := fs.String("addr", defaultAddr, "the loopback `address` to listen on")
	logDir := fs.String("log-dir", "", "the `directory` of the steps' logs (default $XDG_STATE_HOME/crew-actions/logs)")
	if code, ok := parseFlags(fs, args, stderr); !ok {
		return code
	}
	if fs.NArg() != 0 {
		return usageError(stderr, "engine takes no argument")
	}
	// The address is checked as bound, so :7700, or a localhost that
	// resolves elsewhere, is refused too, before any database is opened.
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", *addr)
	if err != nil {
		errorf(stderr, "engine: %v", err)
		return exitFailure
	}
	defer func() { _ = ln.Close() }()
	if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		errorf(stderr, "engine: --addr %s listens on %s, which is not loopback: "+
			"the engine has no authentication yet, so it listens on loopback only", *addr, ln.Addr())
		return exitUsage
	}
	databaseURL := os.Getenv(databaseURLEnv)
	if databaseURL == "" {
		errorf(stderr, "engine: set %s to the URL of the engine's database", databaseURLEnv)
		return exitUsage
	}
	if *logDir == "" {
		if *logDir, err = defaultLogDir(); err != nil {
			errorf(stderr, "engine: %v; set --log-dir", err)
			return exitUsage
		}
	}
	return serveEngine(ctx, ln, databaseURL, *logDir, stdout, stderr)
}

// defaultLogDir returns the directory of the steps' logs:
// $XDG_STATE_HOME/crew-actions/logs, or ~/.local/state/crew-actions/logs
// when XDG_STATE_HOME is unset.
func defaultLogDir() (string, error) {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "crew-actions", "logs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the logs' directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "crew-actions", "logs"), nil
}

// serveEngine serves the engine on ln, with the database at databaseURL and
// the steps' logs under logDir, until ctx ends. It then stops what waits
// for work, then serving, and only then closes the store.
func serveEngine(ctx context.Context, ln net.Listener, databaseURL, logDir string, stdout, stderr io.Writer) int {
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		errorf(stderr, "engine: %v", err)
		return exitFailure
	}
	defer store.Close()
	// Every start migrates; goose's lock lets several engines start at once.
	if err := store.Migrate(ctx); err != nil {
		errorf(stderr, "engine: %v", err)
		return exitFailure
	}
	id, err := store.DatabaseID(ctx)
	if err != nil {
		errorf(stderr, "engine: %v", err)
		return exitFailure
	}
	stop, stopEngine := context.WithCancel(context.WithoutCancel(ctx))
	defer stopEngine()
	e := engine.New(stop, store, logs.New(logDir, id))
	srv := &http.Server{
		Handler:           connect.NewHandler(e.Runners(), e.Clients()),
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          log.New(stderr, "crew-actions: engine: ", 0),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	_, _ = fmt.Fprintf(stdout, "crew-actions: engine serving on http://%s without authentication: "+
		"any local user or process can reach it and have its runners run commands\n", ln.Addr())
	select {
	case err := <-served:
		errorf(stderr, "engine: %v", err)
		return exitFailure
	case <-ctx.Done():
	}
	// Next returns nothing and Follow ends as unavailable, so the calls in
	// flight end before Shutdown waits for them.
	stopEngine()
	return shutdown(context.WithoutCancel(ctx), srv, stderr)
}

// shutdown stops srv serving, waiting up to shutdownTimeout for the calls in
// flight, and closes every connection when they outlast it.
func shutdown(ctx context.Context, srv *http.Server, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		errorf(stderr, "engine: stop serving: %v", err)
		return exitFailure
	}
	return exitOK
}
