package main

import (
	"strings"
	"testing"
)

func TestBuildOpenURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		args []string
		want string
		err  bool
	}{
		{"dashboard alias goes to inbox", "http://localhost:8080", []string{"dashboard"}, "http://localhost:8080/inbox", false},
		{"home alias goes to inbox", "http://localhost:8080", []string{"home"}, "http://localhost:8080/inbox", false},
		{"inbox", "http://localhost:8080", []string{"inbox"}, "http://localhost:8080/inbox", false},
		{"activity", "http://localhost:8080", []string{"activity"}, "http://localhost:8080/activity", false},
		{"agents list", "http://localhost:8080", []string{"agents"}, "http://localhost:8080/agents", false},
		{"agent detail", "http://localhost:8080", []string{"agent", "viktor"}, "http://localhost:8080/crews?agent=viktor", false},
		{"crew detail", "http://localhost:8080", []string{"crew", "backend-team"}, "http://localhost:8080/crews?crew=backend-team", false},
		{"chat by agent slug", "http://localhost:8080", []string{"chat", "viktor"}, "http://localhost:8080/chat/viktor", false},
		{"mission timeline", "http://localhost:8080", []string{"mission", "MIS-42"}, "http://localhost:8080/missions/MIS-42/timeline", false},
		{"journal", "http://localhost:8080", []string{"journal"}, "http://localhost:8080/journal", false},
		{"approvals", "http://localhost:8080", []string{"approvals"}, "http://localhost:8080/approvals", false},
		{"integrations", "http://localhost:8080", []string{"integrations"}, "http://localhost:8080/integrations", false},
		{"routines", "http://localhost:8080", []string{"routines"}, "http://localhost:8080/routines", false},
		{"issues list", "http://localhost:8080", []string{"issues"}, "http://localhost:8080/issues", false},
		{"issue detail", "http://localhost:8080", []string{"issues", "ENG-7"}, "http://localhost:8080/issues/ENG-7", false},
		{"runs", "http://localhost:8080", []string{"runs"}, "http://localhost:8080/runs", false},
		{"settings", "http://localhost:8080", []string{"settings"}, "http://localhost:8080/settings", false},
		{"admin", "http://localhost:8080", []string{"admin"}, "http://localhost:8080/admin", false},
		{"audit", "http://localhost:8080", []string{"audit"}, "http://localhost:8080/audit", false},
		{"credentials", "http://localhost:8080", []string{"credentials"}, "http://localhost:8080/credentials", false},
		{"agent missing id", "http://localhost:8080", []string{"agent"}, "", true},
		{"chat missing id", "http://localhost:8080", []string{"chat"}, "", true},
		{"unknown resource", "http://localhost:8080", []string{"bogus"}, "", true},
		{"trailing slash trimmed", "http://localhost:8080/", []string{"agents"}, "http://localhost:8080/agents", false},
		// Path-segment escaping still has a subject: `mission` is now one of
		// the resources that interpolates into a path rather than a query.
		{"escapes special chars", "http://localhost:8080", []string{"mission", "weird:slug"}, "http://localhost:8080/missions/weird:slug/timeline", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildOpenURL(c.base, c.args)
			if c.err {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// `crewship open agent <slug>` used to build `<server>/agents/<slug>`, and
// there is no such page: the agent detail route was folded into the /crews
// canvas, so the export has agents.html and nothing under agents/ — no
// `[id]` segment, no `agents/_.html` placeholder. StaticFileHandler's last
// resort is the SPA fallback, so the URL served the DASHBOARD under a path
// that says agents. That is the dead-link class already repaired twice on
// this branch (app/(dashboard)/agents/page.tsx documents the last one).
//
// The live convention is /crews?agent=<slug>. Three things are pinned:
//
//	· the roster, `open agents`, still points at /agents — a real route whose
//	  only remaining job is to redirect to /crews for this command's sake;
//	· the argument is query-escaped, not path-escaped. url.PathEscape leaves
//	  `&` and `=` alone because they are legal in a path segment, and one of
//	  those in a slug would silently split the query string;
//	· the argument reaches the canvas as a SLUG. An id is not rejected by
//	  shape: cmd_helpers.go's looksLikeCUID documents that a real slug can be
//	  21+ lowercase-alphanumeric characters starting with 'c' (#1075), so a
//	  shape test would refuse legitimate agents to catch a case whose only
//	  penalty is the roster opening with nothing selected. The contract narrows
//	  in the help text and docs/cli/open.mdx instead.
func TestBuildOpenURL_AgentTargetsTheCrewsCanvas(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"slug selects on the canvas", []string{"agent", "viktor"}, "http://localhost:8080/crews?agent=viktor"},
		{"hyphenated slug", []string{"agent", "data-analyst"}, "http://localhost:8080/crews?agent=data-analyst"},
		{
			"query-escaped, not path-escaped",
			[]string{"agent", "a&b=c"},
			"http://localhost:8080/crews?agent=a%26b%3Dc",
		},
		{
			// A CUID-shaped slug must still build a URL. This is the #1075 case
			// the shape test would have refused.
			"a slug that looks like an id is still a slug",
			[]string{"agent", "customersuccessemea42"},
			"http://localhost:8080/crews?agent=customersuccessemea42",
		},
		// The roster is a separate resource and keeps its own route — /agents
		// exists solely to redirect there (app/(dashboard)/agents/page.tsx).
		{"roster unchanged", []string{"agents"}, "http://localhost:8080/agents"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildOpenURL("http://localhost:8080", c.args)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			// Whatever it builds, it must never be the dead /agents/<x> shape.
			if strings.HasPrefix(got, "http://localhost:8080/agents/") {
				t.Errorf("got %q — /agents/<x> has no route and falls through to the SPA root", got)
			}
		})
	}
}

// `crewship open crew <slug>` had the identical defect to `open agent`, one
// line below the fix for it: it built `<server>/crews/<id>`, and there is no
// such page. app/(dashboard)/crews/ holds page.tsx and nothing else — no
// `[id]` segment — so the export has crews.html and nothing under crews/,
// and StaticFileHandler's SPA fallback served the DASHBOARD under a URL that
// said crews. Same class, same repair: the /crews canvas selects a crew from
// `?crew=<slug>` (hooks/use-crews-selection.tsx, whose selectCrew writes that
// param and clears ?agent).
//
// The three properties pinned for `agent` hold here for the same reasons:
//
//   - the list resource, `open crews`, still points at bare /crews — a real
//     page, and the canvas with nothing selected is exactly what it means;
//   - the argument is query-escaped, not path-escaped, because `&` and `=`
//     are legal in a path segment and PathEscape would leave either one to
//     split the query string;
//   - an id is not rejected by shape. cmd_helpers.go's looksLikeCUID carries
//     the #1075 scar: a real slug can be 21+ lowercase-alphanumeric
//     characters starting with 'c', so a shape test refuses legitimate crews
//     to catch a case whose only penalty is the canvas opening unselected.
//     `open` resolves URLs offline and without auth, so it cannot translate
//     an id either; the contract narrows in the help text and docs instead.
func TestBuildOpenURL_CrewTargetsTheCrewsCanvas(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"slug selects on the canvas", []string{"crew", "backend-team"}, "http://localhost:8080/crews?crew=backend-team"},
		{
			"query-escaped, not path-escaped",
			[]string{"crew", "a&b=c"},
			"http://localhost:8080/crews?crew=a%26b%3Dc",
		},
		{
			// The #1075 case a shape test would have refused.
			"a slug that looks like an id is still a slug",
			[]string{"crew", "customersuccessemea42"},
			"http://localhost:8080/crews?crew=customersuccessemea42",
		},
		// The list is a separate resource and keeps the bare route.
		{"crew list unchanged", []string{"crews"}, "http://localhost:8080/crews"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildOpenURL("http://localhost:8080", c.args)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			// Whatever it builds, it must never be the dead /crews/<x> shape.
			if strings.HasPrefix(got, "http://localhost:8080/crews/") {
				t.Errorf("got %q — /crews/<x> has no route and falls through to the SPA root", got)
			}
		})
	}
}

// Neither dead shape may come back through any resource in the map. This is
// the guard that would have caught `crew` when `agent` was repaired: it walks
// every resource the command accepts rather than the two anybody thought to
// re-check.
func TestBuildOpenURL_NoResourceBuildsADeadDetailRoute(t *testing.T) {
	// Every resource in buildOpenURL's switch, with an argument where one is
	// required. Keep in step with the switch — a new resource belongs here.
	resources := [][]string{
		{"dashboard"}, {"home"}, {"inbox"}, {"activity"},
		{"agents"}, {"agent", "viktor"},
		{"crews"}, {"crew", "backend-team"},
		{"chat", "viktor"}, {"mission", "MIS-42"},
		{"journal"}, {"approvals"}, {"integrations"}, {"routines"},
		{"issues"}, {"issues", "ENG-7"},
		{"runs"}, {"settings"}, {"admin"}, {"audit"}, {"credentials"},
	}
	// The export has agents.html and crews.html and no directory under
	// either, so anything below those prefixes hits the SPA fallback.
	dead := []string{"http://x/agents/", "http://x/crews/"}
	for _, args := range resources {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			got, err := buildOpenURL("http://x", args)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			for _, prefix := range dead {
				if strings.HasPrefix(got, prefix) {
					t.Errorf("open %v built %q — %s<x> has no route and falls through to the SPA root",
						args, got, strings.TrimPrefix(prefix, "http://x"))
				}
			}
		})
	}
}

func TestBuildOpenURL_CaseInsensitive(t *testing.T) {
	got, err := buildOpenURL("http://localhost:8080", []string{"JOURNAL"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/journal") {
		t.Errorf("got %q, want /journal suffix", got)
	}
}
