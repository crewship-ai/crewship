package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// TestRoutesWithNoCLICaller (#2147) runs cli_route_contract_test.go's
// extractor the OTHER direction. TestCLICallsHitRegisteredRoutes asks "does
// every CLI call site hit a registered route?" — this asks the complementary
// question: "does every registered route have a CLI call site?"
//
// It reuses collectAPIRoutes and collectCLICallSites verbatim rather than
// re-scanning the tree, because a hand-rolled path-prefix scan is
// method-blind and misses routes registered through helpers entirely — e.g.
// GET /api/v1/agent-load and GET /api/v1/crewshipd, both registered via
// r.mux.Handle(...) wrapped in authed(wsCtx(...)), read fine by go/ast but
// easy to miss by grep because nothing about them says "route" on the
// surface. Reusing the real machinery is the only way the count is honest.
//
// Three kinds of "no CLI call site resolves to this key" do NOT mean "the
// CLI can't do this":
//
//  1. /api/v1/internal/* — sidecar IPC (crewshipd talking to its own API),
//     never meant for interactive CLI use. Excluded wholesale.
//
//  2. dynamicPathExceptions — already declared and justified in
//     cli_route_contract_test.go. A runtime-chosen final verb (hooks
//     enable/disable, pipeline governance approve/reject/enable/disable)
//     collapses to a `{}` hole that cannot equal the router's literal verb
//     segment, so the CLI call IS there, just unmatchable by exact key.
//     dynamicExceptionCovers below re-derives the concrete routes those
//     entries cover instead of hand-listing them a second time.
//
//  3. browserOrInboundNoCLI — the browser/inbound-token surface: routes
//     answered by something OTHER than a bearer-token-holding CLI process —
//     a browser following a NextAuth redirect, an external system posting to
//     a token-bearing webhook URL, a share-link visitor. Each entry says why.
//
// What's left after all three subtractions is the real number: a registered,
// bearer-token-authed (or intentionally-open) route that a human operating
// the CLI has no command to reach. Run with `-v` to see the full list:
//
//	go test ./cmd/crewship/ -run TestRoutesWithNoCLICaller -v
func TestRoutesWithNoCLICaller(t *testing.T) {
	routes := collectAPIRoutes(t)
	sites := collectCLICallSites(t)

	called := map[string]bool{}
	for _, s := range sites {
		called[s.Method+" "+s.Path] = true
	}

	var (
		internalCount int
		dynamicCount  int
		browserCount  int
		trueGaps      []string
	)
	dynamicUsed := map[string]bool{}
	browserUsed := map[string]bool{}

	for key, r := range routes {
		if called[key] {
			continue
		}
		if strings.HasPrefix(r.Pattern, "/api/v1/internal/") {
			internalCount++
			continue
		}
		if why, ok := dynamicExceptionCovers(key); ok {
			dynamicCount++
			dynamicUsed[key] = true
			t.Logf("dynamic-exception covers %s: %s", key, why)
			continue
		}
		if why, ok := browserOrInboundNoCLI[key]; ok {
			browserCount++
			browserUsed[key] = true
			t.Logf("browser/inbound surface, no CLI by design: %s (%s)", key, why)
			continue
		}
		trueGaps = append(trueGaps, fmt.Sprintf("%s  (%s)", key, r.Pos))
	}
	sort.Strings(trueGaps)

	// Keep the exclusion list honest: an entry that no longer names a
	// registered route, or that a CLI call site now reaches, has quietly
	// turned into a standing (and pointless) exemption. Same reasoning
	// dynamicPathExceptions and knownWorkspaceClearingDrift already apply to
	// themselves in cli_route_contract_test.go.
	for key, why := range browserOrInboundNoCLI {
		if browserUsed[key] {
			continue
		}
		if called[key] {
			t.Errorf("stale browserOrInboundNoCLI entry %q (%s): a CLI call site now reaches it — "+
				"delete the entry, it's covered for real.", key, why)
			continue
		}
		if _, registered := routes[key]; !registered {
			t.Errorf("stale browserOrInboundNoCLI entry %q (%s): no such route is registered any more — "+
				"delete the entry.", key, why)
		}
	}

	t.Logf("=== route -> CLI parity, reverse direction (#2147) ===")
	t.Logf("%d routes registered total", len(routes))
	t.Logf("%d have no CLI call site, of which:", internalCount+dynamicCount+browserCount+len(trueGaps))
	t.Logf("  %3d  /api/v1/internal/*            (sidecar IPC, excluded)", internalCount)
	t.Logf("  %3d  covered via dynamicPathExceptions (runtime-verb, already counted as called)", dynamicCount)
	t.Logf("  %3d  browser/inbound-token surface  (excluded, see browserOrInboundNoCLI)", browserCount)
	t.Logf("  %3d  TRUE GAPS — registered route, no CLI command reaches it", len(trueGaps))
	for _, g := range trueGaps {
		t.Logf("       %s", g)
	}
}

// dynamicExceptionCovers reports whether routeKey ("METHOD /path", `{}` for
// dynamic segments — same normalisation collectAPIRoutes/collectCLICallSites
// both use) is one of the concrete verbs a dynamicPathExceptions entry
// collapses over. E.g. the entry "POST /api/v1/hooks/{}/{}" covers both
// "POST /api/v1/hooks/{}/enable" and "POST /api/v1/hooks/{}/disable" — the
// CLI's call site renders the runtime-chosen verb as a hole, so the router's
// literal registrations can never equal it by exact key, but the call is
// genuinely there.
func dynamicExceptionCovers(routeKey string) (string, bool) {
	rParts := strings.SplitN(routeKey, " ", 2)
	if len(rParts) != 2 {
		return "", false
	}
	rMethod, rSegs := rParts[0], strings.Split(rParts[1], "/")

	for exKey, why := range dynamicPathExceptions {
		exParts := strings.SplitN(exKey, " ", 2)
		if len(exParts) != 2 || exParts[0] != rMethod {
			continue
		}
		exSegs := strings.Split(exParts[1], "/")
		if len(exSegs) != len(rSegs) {
			continue
		}
		match := true
		for i := range exSegs {
			if exSegs[i] == "{}" {
				continue
			}
			if exSegs[i] != rSegs[i] {
				match = false
				break
			}
		}
		if match {
			return why, true
		}
	}
	return "", false
}

// browserOrInboundNoCLI is the browser/inbound-token surface: routes that
// exist and are reachable, but not by a CLI process presenting a bearer
// token. Each is answered by something else entirely —
//
//   - a browser following a NextAuth redirect or holding its session cookie
//     (/api/auth/*, the OAuth provider's own redirect back to us)
//   - an external system POSTing to a URL where the high-entropy token IN
//     THE PATH *is* the auth, not a header a CLI would attach
//     (webhooks, waitpoint callbacks, page webhooks)
//   - a share-link visitor with no Crewship account at all (public pages)
//
// Like dynamicPathExceptions, an entry here is a claim checked from both
// sides by the loop above: it must still name an existing, CLI-unreached
// route, or it is deleted for hiding nothing.
var browserOrInboundNoCLI = map[string]string{
	// NextAuth-compat surface (internal/api/nextauth.go) — implements the
	// next-auth/react client SDK's own endpoints. The caller is the Next.js
	// frontend's NextAuth client, never a bearer-token CLI request; a CLI
	// authenticates via /api/v1/auth/{cli-token,pair/redeem} instead.
	"GET /api/auth/error":                 "NextAuth-compat: browser-rendered auth error page",
	"GET /api/auth/providers":             "NextAuth-compat: providers list read by the next-auth client SDK",
	"GET /api/auth/session":               "NextAuth-compat: session read via the browser's cookie",
	"GET /api/auth/signin":                "NextAuth-compat: browser sign-in page",
	"POST /api/auth/callback/credentials": "NextAuth-compat: credentials callback the next-auth client posts to",
	"POST /api/auth/signout":              "NextAuth-compat: browser sign-out, clears the session cookie",
	"POST /api/auth/token/refresh":        "NextAuth-compat: session-cookie refresh, not a CLI bearer-token flow",

	// OAuth provider redirect target — documented in
	// docs/api-reference/integrations.mdx: "a browser redirect target and has
	// no CLI form". `crewship oauth connect` drives the loopback variant
	// instead, which never touches this endpoint.
	"GET /api/v1/oauth/callback": "OAuth provider's own redirect back to us; browser-only by construction (uses a state token, not a session)",

	// Inbound webhook / token-callback surface — the token IN THE PATH is
	// the credential; these are answered by an external system (or, for the
	// agent-trigger route, the platform's own internal loopback caller),
	// never issued as a CLI request.
	"POST /api/v1/webhooks/{}":            "pipeline webhook dispatch: the {token} path segment is the auth, external caller",
	"POST /api/v1/waitpoint-tokens/{}":    "waitpoint completion callback: external system resumes a paused run via a high-entropy token",
	"POST /api/v1/page-webhooks/{}":       "page webhook dispatch — router_pages_webhooks.go: \"the inbound surface, no auth wrapper, by design\"",
	"POST /api/v1/webhooks/{}/{}/trigger": "agent-webhook trigger: internal loopback caller (chatbridge/IPC), not an interactive CLI request",

	// Public share-link surface — no Crewship account, no bearer token; the
	// visitor is a browser holding (at most) the page's own unlock password.
	"GET /api/v1/public/pages/{}":         "public page share link: browser visitor, no account, no CLI credential to present",
	"POST /api/v1/public/pages/{}/unlock": "public page unlock: browser visitor supplying the page's own password, not a CLI session",
}
