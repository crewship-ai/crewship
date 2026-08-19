package api

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// unregisteredSpawnSites lists the `go` statements in this package's
// production code that deliberately do NOT register with
// beginBackgroundWork, each with the reason it does not need to.
//
// Almost every entry is a long-lived daemon — a ticker loop or a
// server owned by the process, stopped through its own context or
// stop-channel. They never finish, so a drain that waited for them
// would hang for its full timeout rather than catch anything; their
// shutdown is crewshipd's business, not a test fixture's.
//
// The key is "<file>:<function>". Adding an entry is a deliberate act
// that has to be justified in review — which is the point. The failure
// mode this guard exists to prevent is the silent one: a new
// request-scoped goroutine lands, nothing registers it, and six weeks
// later a different test family starts failing intermittently (#1596).
var unregisteredSpawnSites = map[string]string{
	// Daemons: started once, live for the process.
	"assignments_running_recovery.go:StartStuckRunningSweeper":   "boot daemon: ticker loop, stopped via ctx",
	"assignments_stuck_sweeper.go:StartStuckQueueSweeper":        "boot daemon: ticker loop, stopped via ctx",
	"credential_rotation.go:StartCredentialRotationExpiryWorker": "boot daemon: stop channel + caller's WaitGroup",
	"pages_on_failure.go:StartPanelFreshnessSweeper":             "boot daemon: ticker loop, stopped via ctx",
	"mcp_registry.go:StartRegistrySyncWorker":                    "boot daemon: stop channel + caller's WaitGroup",
	"oauth_token.go:StartOAuthRefreshWorker":                     "boot daemon: stop channel + caller's WaitGroup",
	"port_expose_registry.go:StartPurger":                        "daemon: purge loop, stopped by the registry",
	"ratelimit.go:NewRateLimiter":                                "daemon: bucket cleanup loop, lives with the limiter",
	"recurring_issue_dispatcher.go:Start":                        "daemon: dispatcher loop, stopped via ctx",
	"crew_provisioning.go:NewProvisioningHandler":                "daemons: job-cleanup + startup/periodic GC loops, stopped via the handler's ctx",

	// Not daemons, but genuinely not drainable — each for its own reason.
	"background.go:waitForBackgroundWork": "the waiter's own helper; registering it would make it wait for itself",
	"oauth_flow.go:Loopback": "loopback callback server: lives until the browser redirect arrives or " +
		"its own timeout fires. Draining would block teardown for that whole window, and it owns " +
		"no test-visible state — it writes only after a redirect a test never sends.",
	"oauth_flow.go:runLoopbackServer": "the same server's shutdown watcher: it blocks on the redirect " +
		"or a 120s timeout, whichever comes first. Registering it (briefly done while writing this " +
		"guard) would make one test that touches the loopback flow stall every later teardown until " +
		"the drain gave up — the exact misattributed failure this whole change removes.",
	"router.go:NewRouter": "one-shot bcrypt warmup: touches no database and no filesystem, so it " +
		"cannot race a teardown. Left unregistered so router construction — which every test does — " +
		"does not add a CPU-bound wait to every teardown.",
}

// TestBackgroundWork_EverySpawnSiteIsAccountedFor walks this package's
// non-test Go files and asserts that every `go` statement either
// registers with beginBackgroundWork or is named in
// unregisteredSpawnSites with a reason.
//
// Why a source-level test rather than trusting review: the defect in
// #1596 is invisible at the call site. A handler that spawns an
// unregistered goroutine looks completely correct — it IS correct for
// production — and the cost lands somewhere else entirely, in another
// test, weeks later, as a flake. Nothing about reading the diff tells
// you it happened. So the check has to be mechanical.
//
// It is deliberately syntactic and shallow: it looks for `go ` at the
// start of a statement and for beginBackgroundWork within the
// enclosing function. That accepts a contrived layout it should
// reject, and the tradeoff is chosen knowingly — a guard simple enough
// that nobody has to debug the guard is worth more here than one that
// parses the AST and gets argued with.
func TestBackgroundWork_EverySpawnSiteIsAccountedFor(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var unaccounted []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(src), "\n")

		fn := ""
		for i, raw := range lines {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(raw, "func ") {
				fn = functionName(raw)
			}
			if !strings.HasPrefix(line, "go ") {
				continue
			}
			key := name + ":" + fn
			if _, allowed := unregisteredSpawnSites[key]; allowed {
				continue
			}
			if registeredNear(lines, i) {
				continue
			}
			unaccounted = append(unaccounted, key+" (line "+strconv.Itoa(i+1)+"): "+line)
		}
	}

	if len(unaccounted) > 0 {
		t.Errorf("goroutine spawn sites that neither register with beginBackgroundWork "+
			"nor appear in unregisteredSpawnSites:\n  %s\n\n"+
			"A detached goroutine that outlives its request also outlives the TEST that drove it, "+
			"where it races that test's teardown — deleting storage dirs mid-write and querying a "+
			"closed DB, surfacing as an unrelated test family failing at random (#1596).\n"+
			"Either register it (see background.go) or add it to unregisteredSpawnSites with the "+
			"reason it cannot be drained.",
			strings.Join(unaccounted, "\n  "))
	}
}

// registeredNear reports whether a beginBackgroundWork registration
// sits close enough to the spawn on line i to belong to it. The
// registration goes immediately before the `go` statement and the
// finish call immediately inside it, so a tight window is enough and
// keeps the guard from crediting an unrelated registration elsewhere
// in a long function.
func registeredNear(lines []string, i int) bool {
	lo := i - 3
	if lo < 0 {
		lo = 0
	}
	hi := i + 4
	if hi > len(lines) {
		hi = len(lines)
	}
	for _, l := range lines[lo:hi] {
		if strings.Contains(l, "beginBackgroundWork") || strings.Contains(l, "defer finish()") {
			return true
		}
	}
	return false
}

// functionName pulls a "<Receiver>.<Name>" or "<Name>" label out of a
// func declaration line, for the allow-list key.
func functionName(decl string) string {
	rest := strings.TrimPrefix(decl, "func ")
	if strings.HasPrefix(rest, "(") {
		if end := strings.Index(rest, ")"); end >= 0 {
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	if paren := strings.Index(rest, "("); paren >= 0 {
		rest = rest[:paren]
	}
	return strings.TrimSpace(rest)
}
