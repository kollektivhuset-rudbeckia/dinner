// Command server runs the Rudbeckia dinner registration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	// Embedding the timezone database lets the container run FROM scratch.
	_ "time/tzdata"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/mail"
	"github.com/O5ten/dinners/internal/setup"
	"github.com/O5ten/dinners/internal/store"
	"github.com/O5ten/dinners/internal/web"
)

// version is stamped at build time with -ldflags.
var version = "dev"

// notifyEvery is how often the server looks for a registration deadline that
// has passed. The deadline is weekly, so minutes of drift are irrelevant and a
// slow tick keeps the log quiet.
const notifyEvery = 5 * time.Minute

func main() {
	var (
		checkConfig = flag.Bool("check-config", false,
			"load and validate the configuration file, then exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
		demoMode    = flag.Bool("demo", false,
			"run a throwaway demo: default passwords, an ongoing season with example registrations, banner on every page")
	)
	flag.Parse()

	if *demoMode {
		os.Setenv("DEMO", "true")
	}

	if *showVersion {
		fmt.Println(version)
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(os.Getenv("LOG_LEVEL")),
	}))
	slog.SetDefault(log)

	if *checkConfig {
		if err := check(log); err != nil {
			log.Error("configuration is not valid", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// check validates the configuration without needing any secrets, so it can run
// in CI and as a pre-flight step before a deploy.
func check(log *slog.Logger) error {
	path := os.Getenv("CONFIG_PATH")
	if path == "" {
		path = "config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	log.Info("configuration is valid",
		"path", path,
		"timezone", cfg.Site.Timezone,
		"weekdays", cfg.Dinner.Weekdays,
		"serving", cfg.Dinner.ServingTime,
		"deadline", fmt.Sprintf("%s %s, %d week(s) before",
			cfg.Deadline.ParsedWeekday(), cfg.Deadline.Time, cfg.Deadline.WeeksBefore),
		"teams", len(cfg.Teams))
	for _, t := range cfg.Teams {
		if t.Email != "" && !auth.ValidEmail(t.Email) {
			return fmt.Errorf("team %q: %q is not an e-mail address", t.Name, t.Email)
		}
		log.Info("team", "name", t.Name, "leader", t.Leader, "email", t.Email)
	}
	if cfg.Season != nil {
		log.Info("season", "name", cfg.Season.Name, "start", cfg.Season.Start, "end", cfg.Season.End)
	} else {
		log.Info("no season in the configuration; add one in the admin view")
	}
	return nil
}

func run(log *slog.Logger) error {
	rt, err := config.LoadRuntime()
	if err != nil {
		return err
	}
	cfg, err := config.Load(rt.ConfigPath)
	if err != nil {
		return err
	}
	log.Info("configuration loaded", "path", rt.ConfigPath, "timezone", cfg.Site.Timezone)

	st, err := store.Open(rt.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stopCtx := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopCtx()

	if rt.Demo {
		n, err := setup.Demo(ctx, st, cfg, time.Now())
		if err != nil {
			return fmt.Errorf("seed demo data: %w", err)
		}
		log.Warn("DEMO MODE — do not use this for a real house",
			"password", rt.Password, "admin_password", rt.AdminPassword,
			"seeded_registrations", n, "database", rt.DBPath)
	} else {
		seeded, err := setup.Bootstrap(ctx, st, cfg)
		if err != nil {
			return fmt.Errorf("first-run setup: %w", err)
		}
		if seeded {
			log.Info("empty database seeded from the configuration file",
				"teams", len(cfg.Teams), "season", cfg.Season != nil)
		}
	}

	secure := strings.HasPrefix(rt.BaseURL, "https://")
	guard := auth.New(rt.Password, rt.AdminPassword, rt.SessionSecret, rt.SessionMaxAge, secure)
	mailer := mail.NewSender(rt.Mail, log)
	if !mailer.Enabled() {
		log.Warn("SMTP is not configured; the matlist notifications will only be logged")
	}
	if !guard.HasAdmin() {
		log.Warn("ADMIN_PASSWORD is not set; the /admin view is unavailable")
	}
	if rt.BaseURLUnset() && !rt.Demo {
		log.Warn("BASE_URL is not set, so every link that leaves the site points at "+
			"this machine: the mail to the cooking team, the address a spreadsheet "+
			"fetches, and the link you give a guest",
			"base_url", rt.BaseURL)
	}

	srv, err := web.New(cfg, rt, st, guard, mailer, log)
	if err != nil {
		return err
	}
	srv.StartNotifier(ctx, notifyEvery)

	httpSrv := &http.Server{
		Addr:              rt.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", rt.ListenAddr, "base_url", rt.BaseURL, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdown)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
