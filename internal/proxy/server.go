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
	Listen              string
	Upstream            string
	UpstreamTLS         string
	LogRewrites         bool
	AutoGrantRoles      bool
	AutoTerminateOnDrop bool
}

// fixupState carries per-connection state for the auto-grant and
// auto-terminate features.
//
//   - sendMu serializes writes to the upstream Frontend so injection can't
//     interleave bytes with a normal client→upstream forward.
//   - swallowMu / swallowing gate the upstream→client pump: while swallowing
//     is true, every backend message is dropped until the next ReadyForQuery
//     (which is also dropped). This is how we hide the response of a Query
//     that the proxy itself injected on behalf of the client.
type fixupState struct {
	masterRole    string
	autoTerminate bool

	pendingMu sync.Mutex
	pending   []string

	sendMu sync.Mutex

	swallowMu  sync.Mutex
	swallowing bool
}

func newFixupState(masterRole string, autoTerminate bool) *fixupState {
	return &fixupState{masterRole: masterRole, autoTerminate: autoTerminate}
}

func (s *fixupState) setSwallow() {
	s.swallowMu.Lock()
	s.swallowing = true
	s.swallowMu.Unlock()
}

// shouldSwallow returns true when msg should be dropped silently. When the
// passed message is a ReadyForQuery, the swallow flag is also cleared so
// subsequent messages start flowing again.
func (s *fixupState) shouldSwallow(msg pgproto3.BackendMessage) bool {
	s.swallowMu.Lock()
	defer s.swallowMu.Unlock()
	if !s.swallowing {
		return false
	}
	if _, ok := msg.(*pgproto3.ReadyForQuery); ok {
		s.swallowing = false
	}
	return true
}

func (s *fixupState) enabled() bool { return s.masterRole != "" }

func (s *fixupState) record(role string) {
	if !s.enabled() || role == "" {
		return
	}
	s.pendingMu.Lock()
	s.pending = append(s.pending, role)
	s.pendingMu.Unlock()
}

func (s *fixupState) drain() []string {
	if !s.enabled() {
		return nil
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := s.pending
	s.pending = nil
	return out
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

	var grantTarget string
	if cfg.AutoGrantRoles {
		grantTarget = startup.Parameters["user"]
	}
	fixup := newFixupState(grantTarget, cfg.AutoTerminateOnDrop)

	errCh := make(chan error, 2)
	go func() { errCh <- pumpUpstreamToClient(frontend, backend, fixup) }()
	go func() { errCh <- pumpClientToUpstream(backend, frontend, cfg, fixup) }()

	first := <-errCh
	_ = upstream.Close()
	_ = client.Close()
	<-errCh
	if errors.Is(first, io.EOF) || errors.Is(first, net.ErrClosed) {
		return nil
	}
	return first
}

func pumpUpstreamToClient(fe *pgproto3.Frontend, be *pgproto3.Backend, fixup *fixupState) error {
	for {
		msg, err := fe.Receive()
		if err != nil {
			return err
		}
		// Drop responses to proxy-injected queries (e.g. pg_terminate_backend
		// before DROP DATABASE) so the client never sees them.
		if fixup.shouldSwallow(msg) {
			continue
		}
		switch m := msg.(type) {
		case *pgproto3.AuthenticationSASL:
			// We terminate the client side in plaintext, so strip channel-
			// binding mechanisms. libpq otherwise refuses with "server offered
			// SCRAM-SHA-256-PLUS authentication over a non-SSL connection".
			filtered := m.AuthMechanisms[:0:0]
			for _, mech := range m.AuthMechanisms {
				if !strings.HasSuffix(mech, "-PLUS") {
					filtered = append(filtered, mech)
				}
			}
			if len(filtered) == 0 {
				return fmt.Errorf("upstream only offered channel-binding SASL mechanisms %v, cannot proxy over plaintext client connection", m.AuthMechanisms)
			}
			m.AuthMechanisms = filtered
			_ = be.SetAuthType(pgproto3.AuthTypeSASL)
		case *pgproto3.AuthenticationSASLContinue:
			_ = be.SetAuthType(pgproto3.AuthTypeSASLContinue)
		case *pgproto3.AuthenticationSASLFinal:
			_ = be.SetAuthType(pgproto3.AuthTypeSASLFinal)
		case *pgproto3.AuthenticationMD5Password:
			_ = be.SetAuthType(pgproto3.AuthTypeMD5Password)
		case *pgproto3.AuthenticationCleartextPassword:
			_ = be.SetAuthType(pgproto3.AuthTypeCleartextPassword)
		case *pgproto3.AuthenticationOk:
			_ = be.SetAuthType(pgproto3.AuthTypeOk)
		case *pgproto3.ReadyForQuery:
			// At every server-idle point, run any queued GRANTs out-of-band on
			// the same backend connection before letting the client see RfQ.
			if pending := fixup.drain(); len(pending) > 0 {
				if err := runGrantFixup(fe, pending, fixup); err != nil {
					slog.Warn("grant fixup error", "err", err)
				}
			}
		}
		be.Send(msg)
		if err := be.Flush(); err != nil {
			return err
		}
	}
}

func pumpClientToUpstream(be *pgproto3.Backend, fe *pgproto3.Frontend, cfg Config, fixup *fixupState) error {
	for {
		msg, err := be.Receive()
		if err != nil {
			return err
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			if role := rewrite.ExtractCreateRole(m.String); role != "" {
				fixup.record(role)
				if cfg.LogRewrites && fixup.enabled() {
					slog.Info("queued grant fixup", "role", role, "protocol", "Query")
				}
			}
			if fixup.autoTerminate {
				if dbname := rewrite.ExtractDropDatabase(m.String); dbname != "" {
					if err := injectTerminate(fe, dbname, fixup); err != nil {
						return err
					}
					if cfg.LogRewrites {
						slog.Info("auto-terminate before DROP DATABASE", "database", dbname, "protocol", "Query")
					}
				}
			}
			if rewritten, applied := rewrite.Apply(m.String); len(applied) > 0 {
				if cfg.LogRewrites {
					slog.Info("rewrote Query", "rules", applied, "before", m.String, "after", rewritten)
				}
				m.String = rewritten
			}
		case *pgproto3.Parse:
			if role := rewrite.ExtractCreateRole(m.Query); role != "" {
				fixup.record(role)
				if cfg.LogRewrites && fixup.enabled() {
					slog.Info("queued grant fixup", "role", role, "protocol", "Parse")
				}
			}
			if fixup.autoTerminate {
				if dbname := rewrite.ExtractDropDatabase(m.Query); dbname != "" {
					if err := injectTerminate(fe, dbname, fixup); err != nil {
						return err
					}
					if cfg.LogRewrites {
						slog.Info("auto-terminate before DROP DATABASE", "database", dbname, "protocol", "Parse")
					}
				}
			}
			if rewritten, applied := rewrite.Apply(m.Query); len(applied) > 0 {
				if cfg.LogRewrites {
					slog.Info("rewrote Parse", "rules", applied, "before", m.Query, "after", rewritten)
				}
				m.Query = rewritten
			}
		}
		fixup.sendMu.Lock()
		fe.Send(msg)
		flushErr := fe.Flush()
		fixup.sendMu.Unlock()
		if flushErr != nil {
			return flushErr
		}
	}
}

// injectTerminate sends a Simple Query that asks the upstream to terminate
// every other backend on dbname. It runs before forwarding the client's DROP
// DATABASE so the DROP doesn't trip over leftover sessions. The response is
// not waited for here — fixup.shouldSwallow drops it on the upstream→client
// pump as it arrives.
func injectTerminate(fe *pgproto3.Frontend, dbname string, fixup *fixupState) error {
	fixup.setSwallow()
	sql := rewrite.BuildTerminate(dbname)
	fixup.sendMu.Lock()
	defer fixup.sendMu.Unlock()
	fe.Send(&pgproto3.Query{String: sql})
	return fe.Flush()
}

// runGrantFixup is called by pumpUpstreamToClient when it's about to forward
// a ReadyForQuery and there are pending CREATE ROLE/USER grants. It holds
// fixup.sendMu (so client→upstream forwards block) and uses the same Frontend
// to send a Simple Query and consume its response, all on the same backend
// connection. Errors from the GRANT are logged but never propagated to the
// client — the original CREATE ROLE/USER already succeeded as far as the
// client is concerned.
func runGrantFixup(fe *pgproto3.Frontend, roles []string, fixup *fixupState) error {
	fixup.sendMu.Lock()
	defer fixup.sendMu.Unlock()

	for _, role := range roles {
		sql := rewrite.BuildGrant(role, fixup.masterRole)
		slog.Info("auto-grant role", "role", role, "to", fixup.masterRole)
		fe.Send(&pgproto3.Query{String: sql})
		if err := fe.Flush(); err != nil {
			return err
		}
		for {
			resp, err := fe.Receive()
			if err != nil {
				return err
			}
			if errResp, ok := resp.(*pgproto3.ErrorResponse); ok {
				slog.Warn("auto-grant statement returned error",
					"role", role,
					"to", fixup.masterRole,
					"code", errResp.Code,
					"message", errResp.Message)
			}
			if _, ok := resp.(*pgproto3.ReadyForQuery); ok {
				break
			}
		}
	}
	return nil
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
