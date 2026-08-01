package orchestrator

import (
	"regexp"
	"strings"
	"testing"
)

// The agent's bearer token must never reach a command line.
//
// Every recipe in these prompts is a shell command an agent copies verbatim.
// When the recipe reads `-H "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"` the
// shell expands the variable into curl's argv, and `/proc/<pid>/cmdline` is mode
// 0444 — world-readable regardless of uid, and printed by a bare `ps`. Crew
// members share one container, so any sibling that runs `ps` while a peer's curl
// is in flight lifts that peer's token; the sidecar derives the acting identity
// from exactly that token (internal/sidecar/identity.go), so the lift is a
// full impersonation.
//
// `hidepid` cannot close this for us: remounting /proc needs CAP_SYS_ADMIN,
// which CapDrop: ALL excludes, and Docker exposes no option (moby#9049). The
// only fix is to keep the token out of argv, so these tests guard that.
//
// The env var itself (/proc/<pid>/environ) is a separate, weaker exposure —
// same-uid only, no `ps` equivalent — and is the accepted baseline until agents
// get distinct uids. Argv is the one that leaks across uids.

// promptRecipes returns every generated prompt that contains shell recipes,
// keyed by the source that produced it.
func promptRecipes() map[string]string {
	members := []CrewMember{
		{Name: "Ada", Slug: "ada", RoleTitle: "engineer"},
		{Name: "Bo", Slug: "bo"},
	}
	return map[string]string{
		"crewshipSystemPreamble": crewshipSystemPreamble,
		"BuildLeadContext":       BuildLeadContext(members, nil),
		"BuildPeerContext":       BuildPeerContext(members, "ada"),
	}
}

// authConfigLine is the one form in which the token may appear: a curl config
// (-K) directive, which curl reads from a file descriptor and never places in
// argv.
var authConfigLine = regexp.MustCompile(`^\s*header = "Authorization: Bearer \$CREWSHIP_AGENT_TOKEN"$`)

// argvFlag matches the curl (and shell) options that put their value straight
// into the child process's argv.
var argvFlag = regexp.MustCompile(`(^|\s)(-H|--header|-d|--data|--data-raw|--data-binary|--data-urlencode|-u|--user|-F|--form|--oauth2-bearer|--url)(\s|=)`)

func TestPromptRecipes_NeverInterpolateTheAgentTokenIntoArgv(t *testing.T) {
	for source, prompt := range promptRecipes() {
		for i, line := range strings.Split(prompt, "\n") {
			// Only an EXPANSION can leak. Prose may name the variable ("your token
			// is in CREWSHIP_AGENT_TOKEN"); a recipe that actually carries the value
			// has to write $NAME or ${NAME}, and that is what is scanned here.
			if !strings.Contains(line, "$CREWSHIP_AGENT_TOKEN") &&
				!strings.Contains(line, "${CREWSHIP_AGENT_TOKEN}") {
				continue
			}
			if argvFlag.MatchString(line) {
				t.Errorf("%s line %d passes the agent token as a command-line argument, "+
					"which publishes it in /proc/<pid>/cmdline to every sibling agent:\n\t%s",
					source, i+1, line)
				continue
			}
			// Belt and braces: even without a recognised flag, the token must sit
			// on a curl-config line and nothing else. A bare `export TOK=…` or a
			// `curl … $CREWSHIP_AGENT_TOKEN` positional would still be argv.
			if !authConfigLine.MatchString(line) {
				t.Errorf("%s line %d mentions the agent token outside a curl -K config "+
					"directive; the only argv-free form is `header = \"Authorization: Bearer $CREWSHIP_AGENT_TOKEN\"`:\n\t%s",
					source, i+1, line)
			}
		}
	}
}

// The exact regression this replaces, spelled out so a re-introduction fails
// loudly rather than sliding past the generic scan above.
func TestPromptRecipes_NoBearerHeaderFlag(t *testing.T) {
	banned := []string{
		`-H "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"`,
		`-H 'Authorization: Bearer $CREWSHIP_AGENT_TOKEN'`,
		`-H "Authorization: Bearer ${CREWSHIP_AGENT_TOKEN}"`,
		`--header "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"`,
		`--oauth2-bearer $CREWSHIP_AGENT_TOKEN`,
	}
	for source, prompt := range promptRecipes() {
		for _, b := range banned {
			if strings.Contains(prompt, b) {
				t.Errorf("%s reintroduced the argv-leaking recipe %q", source, b)
			}
		}
	}
}

// authConfigDirective is the curl config line, matched literally when counting.
const authConfigDirective = `header = "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"`

// splitRecipes cuts a prompt into one segment per curl invocation: each segment
// runs from a `curl ` up to the start of the next one, so a recipe's flags, its
// heredoc and its terminator all land in the same segment and nothing bleeds
// into its neighbour.
//
// Segmenting is the point. Counting these tokens across a whole prompt lets a
// mutation conserve the totals while the wiring is broken — delete the auth
// redirect from one recipe, duplicate it on another, and a prompt-wide count is
// unchanged while the first recipe 401s in production. Found by CodeRabbit on
// the api copy of this test, confirmed by mutation, fixed in both.
func splitRecipes(prompt string) []string {
	parts := strings.Split(prompt, "curl ")
	segments := make([]string, 0, len(parts))
	for _, p := range parts[1:] { // parts[0] is the prose before the first curl
		segments = append(segments, "curl "+p)
	}
	return segments
}

// countLines counts lines equal to want. Used for the heredoc terminator, where
// column 0 is load-bearing: an indented `AUTH` does not end a `<<AUTH` heredoc,
// so a substring match would accept a recipe the shell will not.
func countLines(s, want string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if line == want {
			n++
		}
	}
	return n
}

// authWiring is the per-recipe tally of the four things an fd-3 auth config
// needs. All four are 1 on an authenticated recipe and all four are 0 on an
// unauthenticated one; anything else is a half-converted recipe.
type authWiring struct {
	config     int // header = "…" directive
	flag       int // -K /dev/fd/3
	redirect   int // 3<<AUTH
	terminator int // a line that is exactly AUTH
}

func (w authWiring) total() int { return w.config + w.flag + w.redirect + w.terminator }

func (w authWiring) complete() bool {
	return w.config == 1 && w.flag == 1 && w.redirect == 1 && w.terminator == 1
}

func wiringOf(segment string) authWiring {
	return authWiring{
		config:     strings.Count(segment, authConfigDirective),
		flag:       strings.Count(segment, "-K /dev/fd/3"),
		redirect:   strings.Count(segment, "3<<AUTH"),
		terminator: countLines(segment, "AUTH"),
	}
}

// Structural check on the replacement, asserted PER RECIPE.
//
// Every curl that authenticates must carry its OWN complete fd-3 wiring: one
// config directive, one -K, one redirect, one terminator. A recipe with none of
// the four is an unauthenticated GET (/results, /standup, /connections,
// /mission/templates) and is left alone. Anything in between fails at runtime
// with a 401 rather than a leak, but is still broken.
func TestPromptRecipes_AuthHeaderIsReadFromACurlConfigOnFD3(t *testing.T) {
	for source, prompt := range promptRecipes() {
		for i, segment := range splitRecipes(prompt) {
			w := wiringOf(segment)
			if w.total() == 0 {
				continue // unauthenticated call
			}
			if !w.complete() {
				t.Errorf("%s recipe %d has incomplete fd-3 auth wiring "+
					"(config=%d, -K /dev/fd/3=%d, 3<<AUTH=%d, AUTH terminator=%d; each must be 1). "+
					"Every authenticated recipe needs its own, whatever the prompt-wide totals say:\n%s",
					source, i+1, w.config, w.flag, w.redirect, w.terminator, segment)
			}
		}
	}
}

// How many authenticated recipes each prompt is expected to carry.
//
// The per-recipe check above passes a recipe whose auth was deleted WHOLESALE —
// with all four elements gone it reads as an unauthenticated GET. This pins the
// population so that deletion is caught too. Adding an authenticated recipe
// means bumping the number here, deliberately.
var wantAuthenticatedRecipes = map[string]int{
	// The SIDECAR AUTH worked example, then /keeper/execute, /escalate,
	// /expose-port and /skills/author.
	"crewshipSystemPreamble": 5,
	// /assign, /query, /mission/create, cross-crew /assign, /spawn.
	"BuildLeadContext": 5,
	// /query and /escalate.
	"BuildPeerContext": 2,
}

func TestPromptRecipes_EveryAuthenticatedRecipeIsStillThere(t *testing.T) {
	for source, prompt := range promptRecipes() {
		want, ok := wantAuthenticatedRecipes[source]
		if !ok {
			t.Fatalf("no expected recipe count recorded for %s", source)
		}
		got := 0
		for _, segment := range splitRecipes(prompt) {
			if wiringOf(segment).total() > 0 {
				got++
			}
		}
		if got != want {
			t.Errorf("%s carries %d authenticated recipes, expected %d. If a recipe was "+
				"legitimately added or removed, update wantAuthenticatedRecipes; if one lost "+
				"its auth, it will 401.", source, got, want)
		}
	}
}

// The Keeper preamble names two calls that carry a request body on stdin. Those
// keep `--data @-`; the auth config had to move to fd 3 precisely so stdin
// stays free for the body. Assert both survive in the same recipe.
func TestPreamble_BodyOnStdinStillCoexistsWithTheAuthConfig(t *testing.T) {
	p := crewshipSystemPreamble
	if strings.Count(p, "--data @-") == 0 {
		t.Fatal("the preamble no longer sends any request body over stdin")
	}
	if !strings.Contains(p, "--data @- 3<<AUTH") {
		t.Error("a body-on-stdin recipe does not open the auth config on fd 3; " +
			"`-K -` would consume the stdin the body needs")
	}
}
