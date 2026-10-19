package main

import (
	"strings"
	"testing"
)

// Acceptance for the crew-link operations Settings › Crew links is built on:
// connect two crews, list the connections, set file access with the version
// the UI read, and remove a link. Driven through the built binary.

func crewLinksStub(t *testing.T) (*settingsStub, string) {
	t.Helper()
	stub := &settingsStub{routes: map[string]func(map[string]any) (int, string){
		"GET /api/v1/crews": func(map[string]any) (int, string) {
			return 200, `[{"id":"crew_a","slug":"alpha"},{"id":"crew_b","slug":"beta"}]`
		},
		"GET /api/v1/crew-connections": func(map[string]any) (int, string) {
			return 200, `[{"id":"conn_1","from_crew_slug":"alpha","to_crew_slug":"beta","direction":"one_way","status":"active",` +
				`"forward_file_access":"read","reverse_file_access":"none","access_version":3}]`
		},
	}}
	return stub, profileStubConfig(t, stub.start(t).URL)
}

func TestAcceptance_CrewLinks_ConnectSendsResolvedIDsAndDirection(t *testing.T) {
	stub, cfg := crewLinksStub(t)
	stub.routes["POST /api/v1/crew-connections"] = func(map[string]any) (int, string) { return 201, `{"id":"conn_9"}` }

	out, err := runProfileCLI(t, cfg, "crew", "connect", "alpha", "beta")
	if err != nil {
		t.Fatalf("crew connect: %v\n%s", err, out)
	}
	calls := stub.called("POST", "/api/v1/crew-connections")
	if len(calls) != 1 {
		t.Fatalf("want 1 POST, got %d", len(calls))
	}
	b := calls[0].Body
	if b["from_crew_id"] != "crew_a" || b["to_crew_id"] != "crew_b" {
		t.Errorf("slugs were not resolved to ids: %v", b)
	}
	if _, ok := b["direction"]; !ok {
		t.Errorf("direction missing from body: %v", b)
	}
	if !strings.Contains(out, "conn_9") {
		t.Errorf("output does not name the new connection:\n%s", out)
	}
}

func TestAcceptance_CrewLinks_ListShowsDirectionsAndJSONParses(t *testing.T) {
	_, cfg := crewLinksStub(t)
	out, err := runProfileCLI(t, cfg, "crew", "connections")
	if err != nil {
		t.Fatalf("crew connections: %v\n%s", err, out)
	}
	for _, want := range []string{"conn_1", "alpha", "beta", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	js, err := runProfileCLI(t, cfg, "crew", "connections", "-f", "json")
	if err != nil {
		t.Fatalf("-f json: %v\n%s", err, js)
	}
	if !strings.Contains(js, `"access_version": 3`) && !strings.Contains(js, `"access_version":3`) {
		t.Errorf("json drops access_version, which file-access needs as expected-version:\n%s", js)
	}
}

func TestAcceptance_CrewLinks_FileAccessCarriesExpectedVersion(t *testing.T) {
	stub, cfg := crewLinksStub(t)
	stub.routes["PUT /api/v1/crew-connections/conn_1/file-access"] = func(b map[string]any) (int, string) {
		return 200, `{"id":"conn_1","access_version":4}`
	}
	out, err := runProfileCLI(t, cfg, "crew", "file-access", "conn_1", "alpha", "read_write", "3")
	if err != nil {
		t.Fatalf("file-access: %v\n%s", err, out)
	}
	calls := stub.called("PUT", "/api/v1/crew-connections/conn_1/file-access")
	if len(calls) != 1 {
		t.Fatalf("want 1 PUT, got %d", len(calls))
	}
	if calls[0].Body["expected_version"] != float64(3) {
		t.Errorf("expected_version not sent: %v", calls[0].Body)
	}
}

// A stale version is a 409 from the server; the CLI must fail (exit 5), not
// retry, so the UI's "someone changed this" state has one meaning everywhere.
func TestAcceptance_CrewLinks_StaleFileAccessVersionExitsConflict(t *testing.T) {
	stub, cfg := crewLinksStub(t)
	stub.routes["PUT /api/v1/crew-connections/conn_1/file-access"] = func(map[string]any) (int, string) {
		return 409, `{"error":"stale access_version"}`
	}
	out, err := runProfileCLI(t, cfg, "crew", "file-access", "conn_1", "alpha", "none", "2")
	if err == nil {
		t.Fatalf("a 409 must fail; output: %s", out)
	}
	if got := exitCodeOf(t, err); got != 5 {
		t.Errorf("exit code = %d, want 5 (conflict)", got)
	}
	if n := len(stub.called("PUT", "/api/v1/crew-connections/conn_1/file-access")); n != 1 {
		t.Errorf("CLI retried a stale version: %d PUTs", n)
	}
}

func TestAcceptance_CrewLinks_DisconnectDeletesTheConnection(t *testing.T) {
	stub, cfg := crewLinksStub(t)
	stub.routes["DELETE /api/v1/crew-connections/conn_1"] = func(map[string]any) (int, string) { return 200, `{}` }
	out, err := runProfileCLI(t, cfg, "crew", "disconnect", "conn_1")
	if err != nil {
		t.Fatalf("disconnect: %v\n%s", err, out)
	}
	if len(stub.called("DELETE", "/api/v1/crew-connections/conn_1")) != 1 {
		t.Errorf("no DELETE recorded: %+v", stub.calls)
	}
}
