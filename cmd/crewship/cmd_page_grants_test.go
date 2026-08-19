package main

// cmd_page_grants_test.go — the acceptance test for `crewship page
// grant|revoke|grants` (docs/prd/pages.md §7.1b, §11, §11b decision 13).
//
// Epic #1935. The endpoint half is proved in internal/api/pages_grants_test.go;
// this file proves the client half, which only the CLI can:
//
//   - the three subject flags the PRD names are the three the CLI accepts, and
//     revoke takes every one of them (§11b decision 13 — "an asymmetric revoke
//     is how a grant becomes impossible to remove");
//   - a produce scope reaches the wire as the panel list, not as prose;
//   - the ACL listing shows the server's use-time verdict, INCLUDING the rows
//     that are inert and why, because those are the rows an operator is
//     looking for;
//   - a malformed invocation is refused locally, before any request is sent.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

const pageGrantSlug = "fleet-201"

// runPageGrantCLI is runPageCLI with the subject flags reset first.
//
// The command tree is package-level state and cobra keeps a flag's value
// between Execute calls, so `--crew lookout` in one invocation is still set
// during the next one — which for THIS surface would look like the operator
// naming two subjects at once, and the "exactly one subject" guard would fire
// on a command line that never had two. Production is one process per
// invocation and never sees it; the test has to put the flags back.
func runPageGrantCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	page := findSubcommand(rootCmd.Commands(), "page")
	if page == nil {
		return runPageCLI(t, "", args...)
	}
	for _, name := range []string{"grant", "revoke", "grants"} {
		sub := findSubcommand(page.Commands(), name)
		if sub == nil {
			continue
		}
		sub.Flags().VisitAll(func(f *pflag.Flag) {
			if sv, ok := f.Value.(pflag.SliceValue); ok {
				_ = sv.Replace(nil)
			} else {
				_ = f.Value.Set(f.DefValue)
			}
			f.Changed = false
		})
	}
	return runPageCLI(t, "", args...)
}

// pageGrantsRoute is the one path §11 lists for this surface.
var pageGrantsRoute = "/api/v1/pages/" + pageGrantSlug + "/grants"

// pageGrantStubBody is the envelope every verb answers with: the page and its
// complete ACL after the change.
func pageGrantStubBody() []byte {
	return []byte(`{
		"page": "` + pageGrantSlug + `",
		"grants": [
			{"subject_type":"agent","subject":"watcher","subject_id":"agt_1","level":"produce",
			 "panels":["sluzby"],"granted_by":"ada@example.com","granted_at":"2026-08-12T09:14:22Z","live":true},
			{"subject_type":"user","subject":"bob@example.com","subject_id":"usr_2","level":"read",
			 "granted_by":"lost@example.com","granted_at":"2026-08-11T08:00:00Z","live":false,
			 "inert_reason":"the human who issued it is no longer a member of this workspace"}
		]
	}`)
}

// TestPageCLI_GrantSendsTheSubjectLevelAndScope — the flags of §7.1b reach the
// wire as the fields internal/api/pages_grants.go reads, and nothing else does.
func TestPageCLI_GrantSendsTheSubjectLevelAndScope(t *testing.T) {
	stub := pageStub(t)
	stub.OnPut(pageGrantsRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, pageGrantStubBody(), "application/json"
	})

	out, err := runPageGrantCLI(t, "page", "grant", pageGrantSlug,
		"--agent", "watcher", "--level", "produce", "--panels", "sluzby,zatizeni")
	if err != nil {
		t.Fatalf("page grant: %v\n%s", err, out)
	}

	calls := stub.CallsFor("PUT", pageGrantsRoute)
	if len(calls) != 1 {
		t.Fatalf("PUT %s called %d times, want 1", pageGrantsRoute, len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, string(calls[0].Body))
	}
	for field, want := range map[string]string{
		"subject_type": "agent",
		"subject":      "watcher",
		"level":        "produce",
	} {
		if got, _ := body[field].(string); got != want {
			t.Errorf("body %s = %v, want %q", field, body[field], want)
		}
	}
	panels, _ := body["panels"].([]any)
	if len(panels) != 2 || panels[0] != "sluzby" || panels[1] != "zatizeni" {
		t.Errorf("body panels = %v, want the two ids --panels named, in order", body["panels"])
	}
	// The subject is a REFERENCE the server resolves; the CLI must not be
	// inventing ids or sending the caller's own identity along with it.
	for _, forbidden := range []string{"granted_by", "granted_by_user_id", "subject_id"} {
		if _, present := body[forbidden]; present {
			t.Errorf("the request body carries %q; the issuer is the token's, decided server-side", forbidden)
		}
	}
	if !strings.Contains(out, "produce") || !strings.Contains(out, "agent/watcher") {
		t.Errorf("the confirmation does not say what was granted to whom:\n%s", out)
	}

	// --panels is repeatable as well as comma-separated: a page may carry 24
	// panels, and a shell that is already quoting is one comma from silence.
	stub.ResetCalls()
	if out, err := runPageGrantCLI(t, "page", "grant", pageGrantSlug,
		"--agent", "watcher", "--level", "produce", "--panels", "sluzby", "--panels", "zatizeni"); err != nil {
		t.Fatalf("repeated --panels: %v\n%s", err, out)
	}
	calls = stub.CallsFor("PUT", pageGrantsRoute)
	if len(calls) != 1 {
		t.Fatalf("PUT called %d times, want 1", len(calls))
	}
	_ = json.Unmarshal(calls[0].Body, &body)
	if panels, _ := body["panels"].([]any); len(panels) != 2 {
		t.Errorf("repeated --panels produced %v, want both ids", body["panels"])
	}
}

// TestPageCLI_RevokeIsSymmetricWithGrant — §11b decision 13, in the surface
// where the asymmetry would bite: every subject kind that can be granted can
// be revoked by the same reference, with and without a level.
func TestPageCLI_RevokeIsSymmetricWithGrant(t *testing.T) {
	stub := pageStub(t)
	stub.OnDelete(pageGrantsRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, pageGrantStubBody(), "application/json"
	})

	cases := []struct{ flag, ref, level string }{
		{"agent", "watcher", ""},
		{"crew", "lookout", ""},
		{"user", "ada@example.com", ""},
		{"agent", "watcher", "produce"},
	}
	for _, tc := range cases {
		stub.ResetCalls()
		args := []string{"page", "revoke", pageGrantSlug, "--" + tc.flag, tc.ref}
		if tc.level != "" {
			args = append(args, "--level", tc.level)
		}
		out, err := runPageGrantCLI(t, args...)
		if err != nil {
			t.Fatalf("page revoke --%s: %v\n%s", tc.flag, err, out)
		}
		calls := stub.CallsFor("DELETE", pageGrantsRoute)
		if len(calls) != 1 {
			t.Fatalf("--%s: DELETE called %d times, want 1", tc.flag, len(calls))
		}
		q := calls[0].Query
		for _, want := range []string{"subject_type=" + tc.flag, "subject="} {
			if !strings.Contains(q, want) {
				t.Errorf("--%s: query %q does not carry %q — a revoke whose subject went missing removes nothing or everything",
					tc.flag, q, want)
			}
		}
		if !strings.Contains(q, escapedRef(tc.ref)) {
			t.Errorf("--%s: query %q does not carry the reference %q", tc.flag, q, tc.ref)
		}
		if tc.level == "" {
			if strings.Contains(q, "level=") {
				t.Errorf("--%s: a revoke with no --level sent one anyway (%q); the whole subject is what is meant", tc.flag, q)
			}
		} else if !strings.Contains(q, "level="+tc.level) {
			t.Errorf("--%s: query %q does not carry level=%s", tc.flag, q, tc.level)
		}
	}
}

// escapedRef is the reference as it survives url.Values encoding — an email's
// "@" is percent-encoded, and asserting on the raw string would fail for the
// one subject kind whose reference is not a slug.
func escapedRef(ref string) string {
	return strings.ReplaceAll(ref, "@", "%40")
}

// TestPageCLI_GrantsListShowsTheInertRowsAndWhy — the listing is an audit
// surface, and the row an operator is hunting for is the one that has quietly
// stopped working. Hiding it, or printing "inert" with no reason, would make
// the command worse than the SQL it replaces.
func TestPageCLI_GrantsListShowsTheInertRowsAndWhy(t *testing.T) {
	stub := pageStub(t)
	stub.OnGet(pageGrantsRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusOK, pageGrantStubBody(), "application/json"
	})

	out, err := runPageGrantCLI(t, "page", "grants", pageGrantSlug)
	if err != nil {
		t.Fatalf("page grants: %v\n%s", err, out)
	}
	for _, want := range []string{
		"agent/watcher", "produce", "sluzby",
		"user/bob@example.com", "read",
		"live", "inert",
		"no longer a member of this workspace",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing does not show %q:\n%s", want, out)
		}
	}

	// --format json passes the server's document through: a machine format
	// that re-encodes through a typed struct silently drops the fields this
	// build does not know about.
	out, err = runPageGrantCLI(t, "page", "grants", pageGrantSlug, "--format", "json")
	if err != nil {
		t.Fatalf("page grants --format json: %v\n%s", err, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("--format json did not print JSON: %v\n%s", err, out)
	}
	if got, _ := doc["page"].(string); got != pageGrantSlug {
		t.Errorf("json page = %v, want %q", doc["page"], pageGrantSlug)
	}
}

// TestPageCLI_GrantRefusesAMalformedInvocationLocally — a grant is the one
// command where guessing what the operator meant is unacceptable, so each of
// these fails before a request is sent. The stub's fallback fails the test if
// anything reaches the network.
func TestPageCLI_GrantRefusesAMalformedInvocationLocally(t *testing.T) {
	stub := pageStub(t)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subject", []string{"page", "grant", pageGrantSlug, "--level", "read"}, "--user"},
		{"two subjects", []string{"page", "grant", pageGrantSlug, "--user", "a@b.c", "--crew", "lookout", "--level", "read"}, "exactly one subject"},
		{"no level", []string{"page", "grant", pageGrantSlug, "--agent", "watcher"}, "--level"},
		{"a panel scope on a read grant", []string{"page", "grant", pageGrantSlug, "--agent", "watcher", "--level", "read", "--panels", "sluzby"}, "--panels"},
		{"revoke with no subject", []string{"page", "revoke", pageGrantSlug}, "--user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runPageGrantCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("the command was accepted:\n%s", out)
			}
			combined := err.Error() + out
			if !strings.Contains(combined, tc.want) {
				t.Errorf("the error does not name %q: %v\n%s", tc.want, err, out)
			}
			if calls := len(stub.Calls()); calls != 0 {
				t.Errorf("%d request(s) were sent for an invocation that cannot be valid", calls)
			}
			stub.ResetCalls()
		})
	}
}

// TestPageCLI_GrantSurfacesTheServersRefusal — §7.1b rule 1 is the server's to
// enforce, and the CLI's job is to repeat it in full. A 403 that reached the
// operator as "API error (403)" would send them looking for a role problem.
func TestPageCLI_GrantSurfacesTheServersRefusal(t *testing.T) {
	stub := pageStub(t)
	const refusal = "only a human may issue or revoke a page grant (§7.1b rule 1)"
	stub.OnPut(pageGrantsRoute, func(_ *http.Request, _ []byte) (int, []byte, string) {
		return http.StatusForbidden, []byte(`{"error":"` + refusal + `"}`), "application/json"
	})

	out, err := runPageGrantCLI(t, "page", "grant", pageGrantSlug, "--agent", "watcher", "--level", "write")
	if err == nil {
		t.Fatalf("a 403 was reported as success:\n%s", out)
	}
	if !strings.Contains(err.Error()+out, "only a human") {
		t.Errorf("the server's reason did not reach the operator: %v\n%s", err, out)
	}
}
