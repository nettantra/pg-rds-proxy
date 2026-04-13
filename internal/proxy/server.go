// Package proxy implements the pgwire accept loop and the per-connection
// frontend/backend message pump. Rewrites are applied to Query and Parse
// messages only; everything else passes through unchanged.
package proxy

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/nettantra/pg-rds-proxy/internal/rewrite"
)

type Config struct {
	Listen      string
	Upstream    string
	UpstreamTLS string
	LogRewrites bool
}

func Serve(ctx context.Context, cfg Config) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Listen, err)
	}
	slog.Info("pg-rds-proxy listening",
		"listen", cfg.Listen,
		"upstream", cfg.Upstream,
		"upstream_tls", cfg.UpstreamTLS,
	)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			slog.Warn("accept failed", "err", err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			if err := handle(ctx, c, cfg); err != nil && !errors.Is(err, io.EOF) {
				slog.Debug("connection closed", "remote", c.RemoteAddr().String(), "err", err)
			}
		}()
	}
}

func handle(ctx context.Context, client net.Conn, cfg Config) error {
	backend := pgproto3.NewBackend(client, client)

	var startup *pgproto3.StartupMessage
	for startup == nil {
		msg, err := backend.ReceiveStartupMessage()
		if err != nil {
			return fmt.Errorf("receive startup: %w", err)
		}
		switch m := msg.(type) {
		case *pgproto3.StartupMessage:
			startup = m
		case *pgproto3.SSLRequest:
			if _, err := client.Write([]byte{'N'}); err != nil {
				return err
			}
		case *pgproto3.CancelRequest:
			return forwardCancel(cfg, m)
		default:
			return fmt.Errorf("unexpected pre-startup message %T", msg)
		}
	}

	upstream, err := dialUpstream(ctx, cfg)
	if err != nil {
		return fmt.Errorf("dial upstream: %w", err)
	}
	defer upstream.Close()

	frontend := pgproto3.NewFrontend(upstream, upstream)
	frontend.Send(startup)
	if err := frontend.Flush(); err != nil {
		return fmt.Errorf("forward startup: %w", err)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- pumpUpstreamToClient(frontend, backend) }()
	go func() { errCh <- pumpClientToUpstream(backend, frontend, cfg) }()

	first := <-errCh
	_ = upstream.Close()
	_ = client.Close()
	<-errCh
	if errors.Is(first, io.EOF) || errors.Is(first, net.ErrClosed) {
		return nil
	}
	return first
}

func pumpUpstreamToClient(fe *pgproto3.Frontend, be *pgproto3.Backend) error {
	for {
		msg, err := fe.Receive()
		if err != nil {
			return err
		}
		// The proxy terminates the client connection in plaintext, so we must
		// strip channel-binding SASL mechanisms (SCRAM-SHA-256-PLUS) before
		// forwarding. Otherwise libpq refuses with
		// "server offered SCRAM-SHA-256-PLUS authentication over a non-SSL connection".
		if sasl, ok := msg.(*pgproto3.AuthenticationSASL); ok {
			filtered := sasl.AuthMechanisms[:0:0]
			for _, m := range sasl.AuthMechanisms {
				if !strings.HasSuffix(m, "-PLUS") {
					filtered = append(filtered, m)
				}
			}
			if len(filtered) == 0 {
				return fmt.Errorf("upstream only offered channel-binding SASL mechanisms %v, which cannot be proxied over a plaintext client connection", sasl.AuthMechanisms)
			}
			sasl.AuthMechanisms = filtered
		}
		be.Send(msg)
		if err := be.Flush(); err != nil {
			return err
		}
	}
}

func pumpClientToUpstream(be *pgproto3.Backend, fe *pgproto3.Frontend, cfg Config) error {
	for {
		msg, err := be.Receive()
		if err != nil {
			return err
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			if rewritten, applied := rewrite.Apply(m.String); len(applied) > 0 {
				if cfg.LogRewrites {
					slog.Info("rewrote Query", "rules", applied, "before", m.String, "after", rewritten)
				}
				m.String = rewritten
			}
		case *pgproto3.Parse:
			if rewritten, applied := rewrite.Apply(m.Query); len(applied) > 0 {
				if cfg.LogRewrites {
					slog.Info("rewrote Parse", "rules", applied, "before", m.Query, "after", rewritten)
				}
				m.Query = rewritten
			}
		}
		fe.Send(msg)
		if err := fe.Flush(); err != nil {
			return err
		}
	}
}

func dialUpstream(ctx context.Context, cfg Config) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", cfg.Upstream)
	if err != nil {
		return nil, err
	}
	if cfg.UpstreamTLS == "disable" {
		return raw, nil
	}

	var req [8]byte
	binary.BigEndian.PutUint32(req[0:4], 8)
	binary.BigEndian.PutUint32(req[4:8], 80877103)
	if _, err := raw.Write(req[:]); err != nil {
		raw.Close()
		return nil, fmt.Errorf("send ssl request: %w", err)
	}
	resp := make([]byte, 1)
	if _, err := io.ReadFull(raw, resp); err != nil {
		raw.Close()
		return nil, fmt.Errorf("read ssl response: %w", err)
	}
	if resp[0] != 'S' {
		raw.Close()
		return nil, fmt.Errorf("upstream refused TLS (mode=%s, reply=%q)", cfg.UpstreamTLS, resp[0])
	}
	host, _, _ := net.SplitHostPort(cfg.Upstream)
	tlsCfg := &tls.Config{ServerName: host}
	if cfg.UpstreamTLS != "verify-full" {
		tlsCfg.InsecureSkipVerify = true
	}
	tc := tls.Client(raw, tlsCfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return tc, nil
}

func forwardCancel(cfg Config, m *pgproto3.CancelRequest) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	up, err := dialUpstream(ctx, cfg)
	if err != nil {
		return err
	}
	defer up.Close()
	fe := pgproto3.NewFrontend(up, up)
	fe.Send(m)
	return fe.Flush()
}
