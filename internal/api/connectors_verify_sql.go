// Verify.SQL support for connectors_handler.go's Verify endpoint.
//
// Split into its own file because it pulls in a real database driver
// (github.com/jackc/pgx/v5) that the rest of the connectors handler
// has no reason to depend on — conn_string connectors (Postgres today)
// have no HTTP API to probe the way pat connectors do, so the only way
// to know a submitted host/user/password actually works is to open a
// real connection and let the provider's own auth handshake render the
// verdict. See connectors_handler.go's Verify doc comment and
// crewship-ai/crewship#1204 for the bug this closes: a stdio MCP
// server merely starting never touches the configured credential at
// all, so treating "the process spawned" as "the credential verified"
// is a false positive.
package api

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/crewship-ai/crewship/internal/connectors"
	"github.com/crewship-ai/crewship/internal/httpsafe"
)

// verifySQLTimeout bounds a verify.sql probe end to end — DNS, dial,
// auth handshake, and one round-trip query — so a slow or black-holed
// provider can't hold a server worker indefinitely.
const verifySQLTimeout = 10 * time.Second

// verifySQLDialFunc is the SSRF-safe dialer used for verify.sql
// probes. Same pattern as verifyHTTPClient in connectors_handler.go:
// production always resolves through httpsafe's blocked-CIDR guard;
// only SetVerifySQLDialFuncForTesting swaps it, and only in tests.
var verifySQLDialFunc = httpsafe.SafeDialContext(verifySQLTimeout)

// SetVerifySQLDialFuncForTesting swaps the dialer verify.sql probes
// use, so unit tests can point a manifest's DSN at an in-process fake
// Postgres server on loopback without weakening the production SSRF
// guard (which refuses loopback/RFC1918 by default). Returns a
// restore func; defer it in test bodies. Production code must not
// call this — there is no production wiring path that does.
func SetVerifySQLDialFuncForTesting(d func(ctx context.Context, network, addr string) (net.Conn, error)) (restore func()) {
	prev := verifySQLDialFunc
	verifySQLDialFunc = d
	return func() { verifySQLDialFunc = prev }
}

// probeVerifySQL runs Verify.SQL against the provider: resolve the DSN
// template against the submitted fields, then dispatch to the
// implementation for the manifest's declared driver. Unsupported
// drivers fail closed (ok=false) rather than silently reporting
// ok=true for a check that was never actually performed.
func (h *ConnectorHandler) probeVerifySQL(ctx context.Context, m *connectors.Manifest, fields map[string]string) (bool, string) {
	v := m.Verify.SQL
	rctx := connectors.ResolveContext{Fields: fields}
	dsn, err := m.Resolve(v.DSN, rctx)
	if err != nil {
		return false, "verify DSN resolution failed: " + err.Error()
	}

	switch v.Driver {
	case "postgres":
		return probeVerifyPostgres(ctx, dsn)
	default:
		return false, fmt.Sprintf("verify: unsupported sql driver %q", v.Driver)
	}
}

// probeVerifyPostgres opens a real connection to a Postgres server and
// pings it. There is no "connected but not authenticated" state in the
// Postgres wire protocol — pgx.ConnectConfig performs the full startup
// + authentication handshake as part of establishing the connection —
// so a garbage host, user, or password surfaces here as a real error
// from the network or the server, never as a false ok=true. Ping adds
// one further round trip on top of that so a server that somehow
// accepted the handshake but can't actually serve queries still fails
// the probe.
func probeVerifyPostgres(ctx context.Context, dsn string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, verifySQLTimeout)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return false, "postgres DSN invalid: " + err.Error()
	}
	cfg.DialFunc = verifySQLDialFunc

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return false, "postgres connection failed: " + err.Error()
	}
	defer conn.Close(context.Background())

	if err := conn.Ping(ctx); err != nil {
		return false, "postgres ping failed: " + err.Error()
	}
	return true, ""
}
