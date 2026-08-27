// tracker is the whole product as one binary:
//
//	signum serve    the hosted server, used through the browser
//	signum desktop  the same thing on your own machine: local data, opens
//	                 the browser, works fully signed out
//	signum watch    a headless printer watcher for a machine near the
//	                 printers, reporting to a server
//
// The desktop and the server are one implementation bound to different
// defaults; the watcher and the desktop's printer watching are one
// implementation with different sinks. See agent-docs/architecture.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/basicallysource/signum/internal/blob"
	"github.com/basicallysource/signum/internal/printwatch"
	"github.com/basicallysource/signum/internal/printwatch/driver"
	"github.com/basicallysource/signum/internal/store"
	"github.com/basicallysource/signum/internal/web"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(logger, os.Args[2:], false)
	case "desktop":
		err = serve(logger, os.Args[2:], true)
	case "watch":
		err = watch(logger, os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		logger.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  signum serve    [--addr :8860] [--data DIR] [--identity URL] [--base URL]
  signum desktop  [--addr 127.0.0.1:8860] [--data DIR] [--printers printers.json]
  signum watch    --server URL [--token T] [--interval 10s] --printers printers.json`)
}

// serve runs the web app; desktop mode narrows the defaults to this machine
// and opens the browser.
func serve(logger *slog.Logger, args []string, desktop bool) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	defaultAddr := ":8860"
	if desktop {
		defaultAddr = "127.0.0.1:8860"
	}
	addr := flags.String("addr", envOr("SIGNUM_ADDR", defaultAddr), "listen address")
	data := flags.String("data", envOr("SIGNUM_DATA", ""), "data directory (database and blobs)")
	identity := flags.String("identity", envOr("SIGNUM_IDENTITY", ""), "identity service base URL; empty runs open")
	base := flags.String("base", envOr("SIGNUM_BASE", ""), "this instance's public base URL, for the sign-in redirect")
	printers := flags.String("printers", envOr("SIGNUM_PRINTERS", ""), "JSON file of printers to watch locally")
	mocks := repeated{}
	flags.Var(&mocks, "mock", "simulated printer, name=jobs.json (repeatable)")
	flags.Parse(args)

	dir := *data
	if dir == "" {
		if desktop {
			config, err := os.UserConfigDir()
			if err != nil {
				return fmt.Errorf("no data dir and no user config dir: %w", err)
			}
			dir = filepath.Join(config, "signum")
		} else {
			dir = "data"
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	db, err := store.Open(filepath.Join(dir, "signum.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	if *identity != "" && *base == "" {
		return errors.New("an identity service needs --base (or SIGNUM_BASE) for the sign-in redirect")
	}

	server := &web.Server{
		Store:    db,
		Blobs:    blob.Dir{Root: filepath.Join(dir, "blobs")},
		Engrave:  newEngraver(filepath.Join(dir, "fonts")),
		Identity: strings.TrimSuffix(*identity, "/"),
		BaseURL:  strings.TrimSuffix(*base, "/"),
		Logger:   logger,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The desktop watches printers itself, straight into its own database.
	drivers, err := loadPrinters(*printers, logger)
	if err != nil {
		return err
	}
	drivers = append(drivers, mocks.drivers()...)
	if len(drivers) > 0 {
		watcher := &printwatch.Watcher{
			Drivers: drivers,
			Sink:    sinkFunc(db.RecordJob),
			Logger:  logger,
		}
		go watcher.Run(ctx)
	}

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		logger.Info("signum listening", "version", version, "addr", *addr, "data", dir, "desktop", desktop)
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", "error", err)
			os.Exit(1)
		}
	}()

	if desktop {
		openBrowser(logger, "http://"+hostFor(*addr))
	}

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

// watch polls printers and reports to a server.
func watch(logger *slog.Logger, args []string) error {
	flags := flag.NewFlagSet("watch", flag.ExitOnError)
	server := flags.String("server", envOr("SIGNUM_SERVER", ""), "signum server base URL")
	token := flags.String("token", envOr("SIGNUM_TOKEN", ""), "identity bearer token")
	interval := flags.Duration("interval", 10*time.Second, "poll interval")
	printers := flags.String("printers", envOr("SIGNUM_PRINTERS", ""), "JSON file of printers to watch")
	mocks := repeated{}
	flags.Var(&mocks, "mock", "simulated printer, name=jobs.json (repeatable)")
	flags.Parse(args)

	if *server == "" {
		return errors.New("watch needs --server (or SIGNUM_SERVER)")
	}
	drivers, err := loadPrinters(*printers, logger)
	if err != nil {
		return err
	}
	drivers = append(drivers, mocks.drivers()...)
	if len(drivers) == 0 {
		return errors.New("watch needs at least one printer: --printers printers.json")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	watcher := &printwatch.Watcher{
		Drivers:  drivers,
		Sink:     &printwatch.HTTPSink{URL: strings.TrimSuffix(*server, "/"), Token: *token},
		Interval: *interval,
		Logger:   logger,
	}
	logger.Info("watching", "printers", len(drivers), "server", *server)
	if err := watcher.Run(ctx); !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// repeated collects "--mock name=path" style flags.
type repeated []string

func (r *repeated) String() string { return strings.Join(*r, ",") }
func (r *repeated) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func (r repeated) drivers() []printwatch.Driver {
	var drivers []printwatch.Driver
	for _, spec := range r {
		name, path, ok := strings.Cut(spec, "=")
		if !ok {
			continue
		}
		drivers = append(drivers, &driver.Mock{PrinterName: name, Path: path})
	}
	return drivers
}

// sinkFunc adapts the store's RecordJob to the watcher.
type sinkFunc func(context.Context, printwatch.Job) error

func (f sinkFunc) Record(ctx context.Context, job printwatch.Job) error { return f(ctx, job) }

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// hostFor turns a listen address into something a browser can open.
func hostFor(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

// openBrowser is best-effort; the URL is in the log either way.
func openBrowser(logger *slog.Logger, url string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		logger.Info("open this yourself", "url", url)
	}
}
