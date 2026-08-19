package main

// `crewship memory versions list <path>` and the projection state, driven end
// to end through the BUILT binary against a stub server.
//
// The defect this file exists for: GET /api/v1/memory/versions answers with a
// `projection: {state, reason}` saying whether the path is one the audit trail
// records at all, and the CLI decoded three fields and dropped it. An operator
// therefore saw the SAME empty table for two opposite facts —
//
//	· "this path is recorded and nothing has been written to it yet", and
//	· "nothing records this path, so this list is not evidence of anything" —
//
// which is exactly the confusion the projection field was added to end. The
// CLI is where an agent reads this, so the CLI is where it has to be legible.
//
// Driven through the binary rather than RunE because half of what is asserted
// here — the global --format flag, what lands on stdout versus stderr, the
// exit status — lives outside the function body.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http"
	"net/http/httptest"
)

// memVersionsStub serves the one route the command calls, returning a
// canned body. `raw` is written verbatim so a test can serve a payload with
// NO projection field at all (the older-server case).
func memVersionsStub(t *testing.T, raw string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/memory/versions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("path") == "" {
			t.Errorf("request omitted ?path=: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	}))
}

func memCLIConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath,
		[]byte("token: test-token\nworkspace: c000000000000000000mem\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// The three bodies the server can produce for an EMPTY list. `count` is 0 in
// all three and `entries` is null in all three — the projection is the only
// thing that distinguishes them, which is the whole point.
const (
	memVersionsRecordedEmpty = `{
		"path":"crew:c1/CREW.md","count":0,"entries":null,
		"projection":{"state":"recorded","reason":"Recorded by the memory audit watcher on every write, and by the consolidator for the files it maintains."}
	}`
	memVersionsUnrecordedEmpty = `{
		"path":"agent:martin/lessons.md","count":0,"entries":null,
		"projection":{"state":"unrecorded","reason":"Lessons are written by consolidate.WriteLesson, which does not project into the memory version trail."}
	}`
	memVersionsUnavailableEmpty = `{
		"path":"crew:c1/CREW.md","count":0,"entries":null,
		"projection":{"state":"unavailable","reason":"Memory versioning is switched off on this server (no blob root is configured), so no write of any tier is recorded."}
	}`
	memVersionsRecordedWithRows = `{
		"path":"crew:c1/CREW.md","count":1,
		"entries":[{"id":"v1","path":"crew:c1/CREW.md","tier":"crew","sha256":"abc123def456789","bytes":42,
		            "written_at":"2026-07-03T00:00:00Z","written_by":"consolidator","payload_ref":"blob/ab/abc123"}],
		"projection":{"state":"recorded","reason":"Recorded by the memory audit watcher on every write."}
	}`
)

// The headline assertion: an unreadable history and an empty one must not
// render the same bytes. Everything else in this file is detail.
func TestAcceptance_MemoryVersionsList_EmptyIsNotUnreadable(t *testing.T) {
	bin := buildCrewshipBinary(t)

	recSrv := memVersionsStub(t, memVersionsRecordedEmpty)
	defer recSrv.Close()
	unrecSrv := memVersionsStub(t, memVersionsUnrecordedEmpty)
	defer unrecSrv.Close()

	recorded, err := runCrewship(t, bin, memCLIConfig(t), recSrv.URL,
		"memory", "versions", "list", "crew:c1/CREW.md", "--no-color")
	if err != nil {
		t.Fatalf("recorded run: %v\n%s", err, recorded)
	}
	unrecorded, err := runCrewship(t, bin, memCLIConfig(t), unrecSrv.URL,
		"memory", "versions", "list", "agent:martin/lessons.md", "--no-color")
	if err != nil {
		t.Fatalf("unrecorded run: %v\n%s", err, unrecorded)
	}

	if recorded == unrecorded {
		t.Fatalf("a recorded-but-empty history and an unrecorded one printed identical output — "+
			"the projection state is invisible, which is the defect:\n%s", recorded)
	}
}

// "Recorded and empty" must say the emptiness is a FACT, and must not hedge.
func TestAcceptance_MemoryVersionsList_RecordedEmptySaysNothingWasWritten(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := memVersionsStub(t, memVersionsRecordedEmpty)
	defer srv.Close()

	out, err := runCrewship(t, bin, memCLIConfig(t), srv.URL,
		"memory", "versions", "list", "crew:c1/CREW.md", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing has been written") {
		t.Errorf("a recorded path with no rows must say the history is genuinely empty; got:\n%s", out)
	}
	// It must NOT borrow the language of the unreadable case.
	for _, forbidden := range []string{"not recorded", "cannot be collected"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("recorded output claims %q, which is the opposite of the truth:\n%s", forbidden, out)
		}
	}
}

// "Unrecorded" and "unavailable" must both refuse to present the empty list as
// evidence, and must both pass the server's reason through verbatim — the
// reason is prose written per-path on the server, and paraphrasing it in the
// CLI would put the two out of step the first time one changed.
func TestAcceptance_MemoryVersionsList_UnreadableStatesSayWhy(t *testing.T) {
	bin := buildCrewshipBinary(t)

	cases := []struct {
		name       string
		body       string
		path       string
		wantReason string
	}{
		{
			"unrecorded", memVersionsUnrecordedEmpty, "agent:martin/lessons.md",
			"consolidate.WriteLesson",
		},
		{
			"unavailable", memVersionsUnavailableEmpty, "crew:c1/CREW.md",
			"no blob root is configured",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := memVersionsStub(t, c.body)
			defer srv.Close()

			out, err := runCrewship(t, bin, memCLIConfig(t), srv.URL,
				"memory", "versions", "list", c.path, "--no-color")
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			// The state itself is on the page, so a reader can tell the two
			// unreadable causes apart without decoding the prose.
			if !strings.Contains(out, c.name) {
				t.Errorf("output never names the projection state %q:\n%s", c.name, out)
			}
			if !strings.Contains(out, c.wantReason) {
				t.Errorf("output drops the server's reason (%q):\n%s", c.wantReason, out)
			}
			// The load-bearing sentence: emptiness here is not a finding.
			if !strings.Contains(out, "says nothing about") {
				t.Errorf("output presents an unreadable history as an empty one:\n%s", out)
			}
			// And it must not tell the reader nothing was written.
			if strings.Contains(out, "nothing has been written") {
				t.Errorf("unreadable output claims nothing was written, which it cannot know:\n%s", out)
			}
		})
	}
}

// --format json is what an agent reads. The projection has to survive the
// decode struct, which is where it was being dropped.
func TestAcceptance_MemoryVersionsList_JSONCarriesProjection(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := memVersionsStub(t, memVersionsUnrecordedEmpty)
	defer srv.Close()

	out, err := runCrewship(t, bin, memCLIConfig(t), srv.URL,
		"memory", "versions", "list", "agent:martin/lessons.md", "--format", "json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var decoded struct {
		Count      int `json:"count"`
		Projection struct {
			State  string `json:"state"`
			Reason string `json:"reason"`
		} `json:"projection"`
	}
	if jerr := json.Unmarshal([]byte(out), &decoded); jerr != nil {
		t.Fatalf("json does not parse: %v\n%s", jerr, out)
	}
	if decoded.Projection.State != "unrecorded" {
		t.Errorf("projection.state = %q, want \"unrecorded\" — the field is being dropped on decode", decoded.Projection.State)
	}
	if decoded.Projection.Reason == "" {
		t.Error("projection.reason is empty; the caller cannot tell WHY the history is unreadable")
	}
}

// The version rows themselves. WRITTEN reads `written_at` — the wire field —
// and not `created_at`, which the response has never contained and which
// rendered the column permanently blank.
func TestAcceptance_MemoryVersionsList_RowsShowWhenAndWho(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := memVersionsStub(t, memVersionsRecordedWithRows)
	defer srv.Close()

	out, err := runCrewship(t, bin, memCLIConfig(t), srv.URL,
		"memory", "versions", "list", "crew:c1/CREW.md", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, want := range []string{"abc123def456789", "2026-07-03T00:00:00Z", "consolidator", "42"} {
		if !strings.Contains(out, want) {
			t.Errorf("row output is missing %q:\n%s", want, out)
		}
	}
	// A populated list needs no projection warning — the rows are the answer.
	if strings.Contains(out, "says nothing about") {
		t.Errorf("a recorded list with rows should carry no unreadable warning:\n%s", out)
	}
}

// A server built before the projection field existed answers without it. That
// must read as "recorded" — the state every path had when the only writers
// were the ones that record — and never as "unavailable", which would tell an
// operator their versioning was switched off when it is merely older.
func TestAcceptance_MemoryVersionsList_OlderServerReadsAsRecorded(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := memVersionsStub(t, `{"path":"crew:c1/CREW.md","count":0,"entries":null}`)
	defer srv.Close()

	out, err := runCrewship(t, bin, memCLIConfig(t), srv.URL,
		"memory", "versions", "list", "crew:c1/CREW.md", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "unavailable") || strings.Contains(out, "says nothing about") {
		t.Errorf("a server with no projection field must not be reported as unreadable:\n%s", out)
	}
}
