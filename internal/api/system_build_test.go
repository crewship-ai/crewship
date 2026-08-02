package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/database"
)

// #1645 — GET /api/v1/system/version is the one route that can answer "what
// build is this server running?". Before this change it reported only the
// version string, so a dev slot built by dev.sh (ldflags-less: version
// "dev" for every build ever made from that slot) was indistinguishable
// from itself a week earlier.

func decodeVersionBody(t *testing.T, h *SystemHandler) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/system/version", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "u1"}))
	rr := httptest.NewRecorder()
	h.Version(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestSystemVersion_ReportsTheBuildItWasGiven(t *testing.T) {
	dirty := true
	h := NewSystemHandler(newTestLogger(), "dev").WithBuild(buildinfo.Info{
		Version:   "dev",
		Commit:    "496c8c1a84be761abdb5cbe323a1fd501b8b9ab7",
		BuildTime: "2026-08-02T15:35:24Z",
		Dirty:     &dirty,
		GoVersion: "go1.26",
		OS:        "linux",
		Arch:      "amd64",
	})

	resp := decodeVersionBody(t, h)

	// Each of these is asserted against the exact value the server was built
	// with. "the key is present" would be satisfied by a handler that echoed
	// a zero value, which is the failure this endpoint exists to rule out.
	if resp["commit"] != "496c8c1a84be761abdb5cbe323a1fd501b8b9ab7" {
		t.Errorf("commit=%v want the build's own SHA", resp["commit"])
	}
	if resp["build_time"] != "2026-08-02T15:35:24Z" {
		t.Errorf("build_time=%v want the build's own timestamp", resp["build_time"])
	}
	if resp["dirty"] != true {
		t.Errorf("dirty=%v want true — this build carried uncommitted changes", resp["dirty"])
	}
	if resp["go_version"] != "go1.26" {
		t.Errorf("go_version=%v want go1.26", resp["go_version"])
	}
	if resp["os"] != "linux" || resp["arch"] != "amd64" {
		t.Errorf("os/arch=%v/%v want linux/amd64", resp["os"], resp["arch"])
	}
	// The pre-existing contract must survive: the web UI's update banner
	// reads `current`.
	if resp["current"] != "dev" {
		t.Errorf("current=%v want dev", resp["current"])
	}
}

// A build with ldflags but no VCS stamping (the Dockerfile path) does not
// know whether its tree was dirty. That has to reach the client as null, not
// as a confident `false` — "clean" and "nobody recorded it" are different
// answers and only one of them is safe to act on.
func TestSystemVersion_UnknownDirtyIsNullNotFalse(t *testing.T) {
	h := NewSystemHandler(newTestLogger(), "v1.2.3").WithBuild(buildinfo.Info{
		Version: "v1.2.3",
		Commit:  "cafebabe",
	})

	resp := decodeVersionBody(t, h)

	raw, ok := resp["dirty"]
	if !ok {
		t.Fatal("response omits `dirty` entirely; want an explicit null")
	}
	if raw != nil {
		t.Errorf("dirty=%v want null when nothing stamped vcs.modified", raw)
	}
}

// The schema version the server BINARY expects — the second half of the
// "name both sides" ask in #1645. A running server has already migrated its
// DB up to this number (database.Migrate applies every registered migration
// at boot and refuses to start on a newer one), so the binary's ceiling is
// the schema a client is actually talking to.
func TestSystemVersion_ReportsTheSchemaThisBinaryExpects(t *testing.T) {
	h := NewSystemHandler(newTestLogger(), "dev")

	resp := decodeVersionBody(t, h)

	want := float64(database.MaxKnownMigrationVersion())
	if want <= 0 {
		t.Fatalf("MaxKnownMigrationVersion()=%v — the registry is empty, the assertion below would be vacuous", want)
	}
	if resp["schema_version"] != want {
		t.Errorf("schema_version=%v want %v (the highest migration this binary can apply)", resp["schema_version"], want)
	}
}

// A handler that was never handed build info still has one honest source
// left: the VCS stamps in its own binary. NewRouter seeds exactly that, so a
// server whose wiring forgets SetBuild degrades to "the commit I was compiled
// from" rather than to nothing.
func TestSystemVersion_FallsBackToTheBinarysOwnStamps(t *testing.T) {
	h := NewSystemHandler(newTestLogger(), "dev")

	resp := decodeVersionBody(t, h)

	want := buildinfo.Resolve("dev", "none", "unknown")
	if resp["commit"] != want.Commit {
		t.Errorf("commit=%v want this binary's own %q", resp["commit"], want.Commit)
	}
	if resp["go_version"] != want.GoVersion {
		t.Errorf("go_version=%v want %q", resp["go_version"], want.GoVersion)
	}
}
