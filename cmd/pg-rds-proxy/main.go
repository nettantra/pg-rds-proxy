package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nettantra/pg-rds-proxy/internal/proxy"
)

var (
	version = "dev"
	commit  = "unknown"
)

const defaultConfigPath = "/etc/pg-rds-proxy/pg-rds-proxy.conf"

func main() {
	// Pre-pass: resolve --config before defining the real flag set, so we can
	// load its values into the environment and let the real flag defaults pick
	// them up via env() below. The pre-FlagSet silently ignores unknown flags.
	pre := flag.NewFlagSet("pg-rds-proxy-pre", flag.ContinueOnError)
	pre.SetOutput(io.Discard)
	configPath := pre.String("config", env("PGRP_CONFIG", defaultConfigPath), "")
	_ = pre.Parse(os.Args[1:])

	if err := loadConfigFile(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "load config %s: %v\n", *configPath, err)
		os.Exit(2)
	}

	var (
		cfg         proxy.Config
		logLevel    string
		showVersion bool
	)

	flag.StringVar(configPath, "config", *configPath, "path to config file (KEY=VALUE lines)")
	flag.StringVar(&cfg.Listen, "listen", env("PGRP_LISTEN", "127.0.0.1:5532"), "frontend listen address")
	flag.StringVar(&cfg.Upstream, "upstream", os.Getenv("PGRP_UPSTREAM"), "RDS upstream host:port")
	flag.StringVar(&cfg.UpstreamTLS, "upstream-tls", env("PGRP_UPSTREAM_TLS", "require"), "upstream TLS mode: disable|require|verify-full")
	flag.BoolVar(&cfg.LogRewrites, "log-rewrites", env("PGRP_LOG_REWRITES", "") != "", "log each statement the proxy rewrites")
	flag.BoolVar(&cfg.AutoGrantRoles, "auto-grant-roles", env("PGRP_AUTO_GRANT_ROLES", "") != "", "after CREATE ROLE/USER, run GRANT <newrole> TO <connecting user> on the same connection (RDS workaround for must-be-able-to-SET-ROLE errors)")
	flag.StringVar(&logLevel, "log-level", env("PGRP_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("pg-rds-proxy %s (%s)\n", version, commit)
		return
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(logLevel),
	})))

	if cfg.Upstream == "" {
		slog.Error("--upstream is required (or set PGRP_UPSTREAM)")
		os.Exit(2)
	}

	slog.Info("starting pg-rds-proxy", "version", version, "commit", commit)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := proxy.Serve(ctx, cfg); err != nil {
		slog.Error("proxy exited", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadConfigFile reads a simple KEY=VALUE config file and exports each entry
// into the process environment, unless that variable is already set (so real
// env vars and CLI flags always win over the file). Blank lines and lines
// starting with `#` are ignored. Missing file is not an error.
func loadConfigFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		if _, set := os.LookupEnv(key); !set {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}

func parseLevel(s string) slog.Level {
	switch s {
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
