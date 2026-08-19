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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// journalStub answers the list and count routes the way the server really
// would for an unmatchable entry_type: an empty page, and a zero.
func journalStub(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/journal/count":
			_, _ = w.Write([]byte(`{"total":0}`))
		case r.URL.Path == "/api/v1/journal/stream":
			// An SSE stream that says nothing and never closes — a live tail
			// with no traffic. A `watch` that reached here would hang, which
			// is why the caller below runs it under a deadline.
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-r.Context().Done()
		case strings.HasPrefix(r.URL.Path, "/api/v1/journal"):
			_, _ = w.Write([]byte(`{"entries":[],"next_cursor":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// runCrewshipUntil is runCrewship with a deadline, for the commands that do
// not return on their own. A command that outlives the deadline has NOT
// refused its arguments, so the deadline expiring is a failed assertion rather
// than a flake.
func runCrewshipUntil(t *testing.T, d time.Duration, bin, cfgPath, serverURL string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append(args, "--server", serverURL)...)
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfgPath)
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(out), context.DeadlineExceeded
	}
	return string(out), err
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

// `journal count` shares every filter flag with the list view and none of its
// validation: the guard was wired into the journal root command only, so the
// count subcommand forwarded the glob to `entry_type IN ('approval.*')` and
// printed a bare `0`.
//
// A number is worse than an empty page. There is no "no entries" line to give
// a reader pause and nothing to scroll — an operator quotes it into an audit
// answer as "zero approvals were granted".
func TestAcceptance_JournalCountTypeGlob_IsRefusedNotZero(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := journalStub(t)
	defer srv.Close()

	for _, tc := range []struct {
		name string
		flag string
		arg  string
	}{
		{"type", "--type", "approval.*"},
		{"exclude-type", "--exclude-type", "container.*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCrewship(t, bin, journalCLIConfig(t), srv.URL,
				"journal", "count", tc.flag, tc.arg, "--no-color")
			if err == nil {
				t.Fatalf("`journal count %s %s` exited 0 and printed a count for a filter that "+
					"can only match a literal. Output:\n%s", tc.flag, tc.arg, out)
			}
			if strings.TrimSpace(out) == "0" {
				t.Fatalf("the command answered %q — an operator reads that as an audit fact", out)
			}
			if !strings.Contains(strings.ToLower(out), "wildcard") {
				t.Errorf("the error never names the problem:\n%s", out)
			}
		})
	}
}

// `crewship watch` is `journal --follow` under the name people actually reach
// for, and it had the same hole: the glob went straight into the SSE query and
// the tail sat there showing nothing, forever, which reads as a quiet system.
func TestAcceptance_WatchTypeGlob_IsRefusedNotSilent(t *testing.T) {
	bin := buildCrewshipBinary(t)
	srv := journalStub(t)
	defer srv.Close()

	out, err := runCrewshipUntil(t, 15*time.Second, bin, journalCLIConfig(t), srv.URL,
		"watch", "--type", "approval.*", "--no-color")
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("`watch --type approval.*` streamed instead of refusing — the tail shows "+
			"nothing and the operator reads it as 'nothing is happening'. Output:\n%s", out)
	}
	if err == nil {
		t.Fatalf("`watch --type approval.*` exited 0:\n%s", out)
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
