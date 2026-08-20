// Package clitest provides reusable scaffolds for tests that exercise
// CLI commands (cmd/crewship/cmd_*.go) without spinning up the real
// Crewship server.
//
// The motivating problem: most CLI commands talk to the backend via
// [internal/cli.Client], which is a concrete struct wrapping
// *http.Client. There is no in-package mock — every test that wants
// to verify "the right URL was called with the right body" has to
// hand-roll an httptest.Server + http.HandlerFunc, decode the
// request body, build a canned response, and clean up. That
// boilerplate is the reason most cmd_*.go files only get tested at
// the flag-parsing layer rather than against a realistic backend
// surface.
//
// # When to reach for clitest
//
// Use this package from cmd/crewship/*_test.go (or any other package
// that constructs an [internal/cli.Client]) when you want:
//
//   - to assert "command X issued POST /api/v1/agents with this body"
//     without hand-rolling an httptest.Server;
//   - to exercise error-handling branches against canonical
//     401/403/404/409/422/429/5xx response shapes — the [EdgeCases]
//     catalog enumerates the ones the real backend produces;
//   - to test idempotency / retry / pagination behaviour against a
//     deterministic backend that you fully control.
//
// Do NOT use clitest from production code paths. The package builds
// on top of net/http/httptest and is test-only.
//
// # Edge case catalog
//
// [EdgeCases] enumerates the recurring backend response patterns the
// CLI must handle. Each entry includes the HTTP status, the JSON
// shape the real backend emits, and a one-line recipe showing how to
// install it on a [StubServer]. Adding a new edge case here is the
// preferred pattern for documenting a class of CLI-side bugs the
// test suite should keep regression-proof; the slug (e.g.
// "edge_case_rate_limited_response") becomes searchable across the
// codebase.
//
// # Concurrency
//
// [StubServer] is safe for concurrent use — the route table is
// guarded by a sync.Mutex so a test that fires N concurrent CLI
// commands against the same stub can still assert call counts
// without races flagging.
//
// # Always talk to a stub through StubServer.Client
//
// Never reach a [StubServer] with http.DefaultClient, and never with
// the http.Get / http.Post / http.Head package-level wrappers — those
// ARE http.DefaultClient. Use [StubServer.Client], which is backed by
// the server's own private transport:
//
//	resp, err := stub.Client().Get(stub.URL() + "/api/v1/agents")
//
// and when driving a real [internal/cli.Client], hand it the same one:
//
//	c := cli.NewClient(stub.URL(), token, wsID)
//	c.HTTPClient = stub.Client()
//
// This is not style. httptest.Server.Close — which [StubServer.Close]
// delegates to — calls http.DefaultTransport.CloseIdleConnections() as
// a courtesy to the common case. In a parallel package that means every
// sibling's `defer stub.Close()` empties the shared connection pool
// underneath any request currently in flight. net/http transparently
// replays GET/HEAD/OPTIONS/TRACE, so the damage stays invisible until a
// DELETE, POST, PATCH or PUT is hit, which fails with "net/http:
// HTTP/1.x transport connection broken: http: CloseIdleConnections
// called". That was #2041; TestStubServer_ConnPoolSurvivesSiblingClose
// guards it.
//
// Note that cli.NewClient leaves Transport nil — i.e.
// http.DefaultTransport — so a cli.Client pointed at a stub without the
// assignment above inherits exactly the same exposure.
//
// # Stability
//
// This is INTERNAL test tooling. The exported surface MAY change
// without a deprecation cycle. Treat clitest as you would a snippet
// library, not a public API.
package clitest
