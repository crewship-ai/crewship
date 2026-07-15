// Tests for the Verify.SQL probe (connectors_verify_sql.go) — the
// real-connection check that closes crewship-ai/crewship#1204 for
// conn_string connectors like Postgres. Before this fix,
// ConnectorHandler.Verify treated any manifest without a populated
// Verify.HTTP block as "nothing to check" and returned ok=true
// unconditionally, so a garbage host/user/password for Postgres
// reported success without ever touching the network.
//
// A real *postgres server* isn't available in this test binary, so
// these tests stand up a minimal Postgres wire-protocol listener
// (fakePostgresServer, below) that renders its auth verdict exactly
// where a real server does — immediately after the startup handshake,
// via either AuthenticationOk or an ErrorResponse — which is the same
// signal probeVerifyPostgres depends on.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/crewship-ai/crewship/internal/connectors"
)

const synthPostgresManifest = `id: synth-postgres
name: Synthetic Postgres
description: Test conn_string connector
category: testing
brand: {logo: synth, color: "#336791"}
auth_mode: conn_string
fields:
  - {key: host, label: Host, type: text, required: true}
  - {key: port, label: Port, type: number, required: true, default: "5432"}
  - {key: database, label: Database, type: text, required: true}
  - {key: user, label: User, type: text, required: true}
  - {key: password, label: Password, type: password, required: true}
  - {key: ssl, label: SSL mode, type: select, required: true, default: disable, choices: [disable, require]}
derived:
  dsn: "postgres://${field.user}:${field.password}@${field.host}:${field.port}/${field.database}?sslmode=${field.ssl}"
mcp:
  transport: stdio
  command: echo
  args: ["${derived.dsn}"]
verify:
  sql:
    driver: postgres
    dsn: "${derived.dsn}"
`

const synthUnsupportedSQLDriverManifest = `id: synth-mysql
name: Synthetic MySQL
description: Test conn_string connector with an unimplemented driver
category: testing
brand: {logo: synth, color: "#00758F"}
auth_mode: conn_string
fields:
  - {key: host, label: Host, type: text, required: true}
mcp:
  transport: stdio
  command: echo
verify:
  sql:
    driver: mysql
    dsn: "mysql://${field.host}"
`

// fakePostgresServer starts a minimal Postgres wire-protocol listener
// on loopback and returns its address. accept controls what happens
// right after the client's startup handshake:
//
//   - true:  AuthenticationOk + ReadyForQuery, then answer the
//     follow-up simple-query Ping ("-- ping") with
//     EmptyQueryResponse + ReadyForQuery — a real server's
//     response to a comment-only query.
//   - false: ErrorResponse (SQLSTATE 28P01, invalid_password) —
//     what a real server sends for a rejected credential.
func fakePostgresServer(t *testing.T, accept bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleFakePostgresConn(conn, accept)
		}
	}()

	return ln.Addr().String()
}

func handleFakePostgresConn(conn net.Conn, accept bool) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	backend := pgproto3.NewBackend(conn, conn)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}

	if !accept {
		backend.Send(&pgproto3.ErrorResponse{
			Severity: "FATAL",
			Code:     "28P01",
			Message:  `password authentication failed for user "fake"`,
		})
		_ = backend.Flush()
		return
	}

	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return
	}

	for {
		msg, err := backend.Receive()
		if err != nil {
			return
		}
		switch msg.(type) {
		case *pgproto3.Query:
			backend.Send(&pgproto3.EmptyQueryResponse{})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if err := backend.Flush(); err != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

// dialToFakeServer returns a verify.sql dialer that ignores whatever
// host/port the resolved DSN specifies and always connects to addr —
// the loopback fake server. Mirrors how RewriteRoundTripper retargets
// verify.http tests, without weakening the production SSRF guard
// (which is only ever bypassed here, inside a test, via the exported
// test seam).
func dialToFakeServer(addr string) func(ctx context.Context, network, a string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}

func postgresVerifyRequest(t *testing.T, cat *connectors.Catalog, connectorID string, fields map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewConnectorHandlerWithCatalog(db, logger, cat)

	payload, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/connectors/"+connectorID+"/verify?workspace_id="+wsID, bytes.NewReader(payload))
	req.SetPathValue("connectorId", connectorID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Verify(rr, req)
	return rr
}

func loadSynthPostgresCatalog(t *testing.T) *connectors.Catalog {
	t.Helper()
	cat, errs := connectors.LoadAll(fstest.MapFS{
		"fixtures/synth-postgres.yaml": &fstest.MapFile{Data: []byte(synthPostgresManifest)},
	})
	if len(errs) != 0 {
		t.Fatalf("load: %v", errs)
	}
	return cat
}

func postgresFields() map[string]string {
	return map[string]string{
		"host":     "127.0.0.1", // literal IP: no real DNS lookup needed before DialFunc runs
		"port":     "5432",
		"database": "verifydb",
		"user":     "fake",
		"password": "garbage",
		"ssl":      "disable",
	}
}

func TestConnectors_Verify_SQLPostgres_GarbageCredentialsRejected(t *testing.T) {
	addr := fakePostgresServer(t, false)
	restore := SetVerifySQLDialFuncForTesting(dialToFakeServer(addr))
	defer restore()

	rr := postgresVerifyRequest(t, loadSynthPostgresCatalog(t), "synth-postgres", postgresFields())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.OK {
		t.Error("expected ok=false for a Postgres server that rejects the credential")
	}
	if resp.Message == "" {
		t.Error("expected a human-readable message on failure")
	}
}

func TestConnectors_Verify_SQLPostgres_ValidCredentialsAccepted(t *testing.T) {
	addr := fakePostgresServer(t, true)
	restore := SetVerifySQLDialFuncForTesting(dialToFakeServer(addr))
	defer restore()

	rr := postgresVerifyRequest(t, loadSynthPostgresCatalog(t), "synth-postgres", postgresFields())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if !resp.OK {
		t.Errorf("expected ok=true for a Postgres server that accepts the credential, message: %s", resp.Message)
	}
}

func TestConnectors_Verify_SQLPostgres_UnreachableHost(t *testing.T) {
	restore := SetVerifySQLDialFuncForTesting(func(_ context.Context, _, _ string) (net.Conn, error) {
		return nil, errors.New("dial tcp: lookup nonexistent-host-xyz123.invalid: no such host")
	})
	defer restore()

	fields := postgresFields()
	fields["host"] = "nonexistent-host-xyz123.invalid"
	rr := postgresVerifyRequest(t, loadSynthPostgresCatalog(t), "synth-postgres", fields)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.OK {
		t.Error("expected ok=false for an unreachable Postgres host")
	}
	if resp.Message == "" {
		t.Error("expected a human-readable message on failure")
	}
}

func TestConnectors_Verify_SQLUnsupportedDriver_FailsClosed(t *testing.T) {
	// A driver the implementation doesn't know how to speak must not
	// silently report ok=true — that would just be the #1204 bug under
	// a new name for the next database connector added to the catalog.
	cat, errs := connectors.LoadAll(fstest.MapFS{
		"fixtures/synth-mysql.yaml": &fstest.MapFile{Data: []byte(synthUnsupportedSQLDriverManifest)},
	})
	if len(errs) != 0 {
		t.Fatalf("load: %v", errs)
	}

	rr := postgresVerifyRequest(t, cat, "synth-mysql", map[string]string{"host": "db.example.com"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.OK {
		t.Error("expected ok=false for an unsupported sql driver")
	}
	if resp.Message == "" || !bytes.Contains([]byte(resp.Message), []byte("mysql")) {
		t.Errorf("expected message to name the unsupported driver, got %q", resp.Message)
	}
}

// TestConnectors_Verify_SQLPostgres_RealCatalogManifest exercises the
// *shipped* postgres.yaml fixture end to end (not a synthetic stand-
// in) against the fake server, guarding against the manifest and the
// handler silently drifting apart (e.g. a future edit to postgres.yaml
// renaming a field the derived.dsn template depends on).
func TestConnectors_Verify_SQLPostgres_RealCatalogManifest(t *testing.T) {
	addr := fakePostgresServer(t, false)
	restore := SetVerifySQLDialFuncForTesting(dialToFakeServer(addr))
	defer restore()

	cat, errs := connectors.LoadAll(connectors.FixturesFS)
	if len(errs) != 0 {
		t.Fatalf("load real catalog: %v", errs)
	}

	rr := postgresVerifyRequest(t, cat, "postgres", postgresFields())
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.OK {
		t.Error("expected ok=false for the real postgres.yaml manifest with a rejecting server — this is the exact #1204 regression")
	}
}
