package main

// `crewship journal --type` and the wildcard that silently matches nothing,
// driven through the BUILT binary.
//
// The trust entries this branch added (approval.trust_granted /
// approval.trust_revoked) are reachable only through --type, and
// docs/cli/routine.mdx invites the reader to filter on the `approval.*` family.
// A reader who types that literally gets a clean exit and ZERO rows: --type is
// an unvalidated passthrough client-side and server-side, and
// internal/journal/queries.go compiles Types into `entry_type IN (...)` with no
// LIKE, prefix or glob path anywhere. `-q` is no escape either — fts5Phrase
// neutralises `*`.
//
// An empty journal and an unmatchable filter printing the same thing is the
// same defect as the memory projection two files over: "nothing happened" and
// "your question could not be asked" are different answers, and the operator
// investigating who disarmed a gate is exactly the person who must not confuse
// them.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func journalCLIConfig(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	if err := os.WriteFile(cfgPath,
		[]byte("token: test-token\nworkspace: c00000000000000000jrn\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// journalStub answers the list route with an empty page, which is what the
// server really would return for an unmatchable entry_type.
func journalStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v1/journal") {
			_, _ = w.Write([]byte(`{"entries":[],"next_cursor":null}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// A wildcard in --type must be refused up front, not forwarded to a filter
// that can only ever match a literal.
func TestAcceptance_JournalTypeGlob_IsRefusedNotSilentlyEmpty(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := journalStub(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, journalCLIConfig(t), srv.URL,
		"journal", "--type", "approval.*", "--no-color")
	if err == nil {
		t.Fatalf("`--type approval.*` exited 0 — an operator reads the empty result as "+
			"'nobody ever granted trust'. Output:\n%s", out)
	}
	// The message has to do the teaching: say the wildcard is not supported
	// AND hand over the shape that works, or the reader is left guessing.
	lower := strings.ToLower(out)
	if !strings.Contains(lower, "wildcard") {
		t.Errorf("the error never names the problem:\n%s", out)
	}
	if !strings.Contains(out, ",") || !strings.Contains(lower, "approval.") {
		t.Errorf("the error does not show the comma-separated form that works:\n%s", out)
	}
}

// The same trap on the exclusion side, which is the one used to hide
// container.metrics noise and would quietly stop excluding anything.
func TestAcceptance_JournalExcludeTypeGlob_IsRefused(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := journalStub(t)
	defer srv.Close()

	out, err := runCrewship(t, bin, journalCLIConfig(t), srv.URL,
		"journal", "--exclude-type", "container.*", "--no-color")
	if err == nil {
		t.Fatalf("`--exclude-type container.*` exited 0; the filter excluded nothing:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "wildcard") {
		t.Errorf("the error never names the problem:\n%s", out)
	}
}

// The literal enumeration the docs should be teaching must keep working
// untouched — the guard rejects the glob, not the dot.
func TestAcceptance_JournalTypeLiteral_ReachesTheServer(t *testing.T) {
	bin := buildCrewshipBinary(t)

	var sawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v1/journal") {
			sawQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"entries":[],"next_cursor":null}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	out, err := runCrewship(t, bin, journalCLIConfig(t), srv.URL,
		"journal", "--type", "approval.trust_granted,approval.trust_revoked", "--no-color")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(sawQuery, "entry_type=approval.trust_granted%2Capproval.trust_revoked") {
		t.Errorf("the literal type list did not reach the server: query was %q", sawQuery)
	}
}
