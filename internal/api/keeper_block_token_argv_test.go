package api

import (
	"regexp"
	"strings"
	"testing"
)

// The agent's bearer token must never reach a command line — the Keeper block's
// half of the invariant.
//
// Same defect, same reasoning as
// internal/orchestrator/prompt_token_argv_test.go, which guards the recipes in
// the system preamble and the lead/peer blocks. Both blocks land in the SAME
// system prompt, so a leak here is worth exactly as much to an attacker as a
// leak there: the shell expands the variable into curl's argv,
// /proc/<pid>/cmdline is mode 0444 — world-readable regardless of uid and
// printed by a bare `ps` — and crew members share one container. The sidecar
// resolves the acting identity from that bearer (internal/sidecar/identity.go),
// so lifting it is a full impersonation of the peer.
//
// The scan is duplicated rather than shared because the two prompt builders are
// unexported in different packages and neither is importable from the other's
// test. If a THIRD package ever grows a recipe, extract this into a small
// shared helper instead of copying it again.

// keeperAuthConfigLine is the one form in which the token may appear: a curl
// config (-K) directive, which curl reads from a file descriptor and never
// places in argv.
var keeperAuthConfigLine = regexp.MustCompile(`^\s*header = "Authorization: Bearer \$CREWSHIP_AGENT_TOKEN"$`)

// keeperArgvFlag matches the curl options that put their value straight into
// the child process's argv.
var keeperArgvFlag = regexp.MustCompile(`(^|\s)(-H|--header|-d|--data|--data-raw|--data-binary|--data-urlencode|-u|--user|-F|--form|--oauth2-bearer|--url)(\s|=)`)

// keeperBlockFixture renders the block the way an agent actually receives it.
func keeperBlockFixture(t *testing.T) string {
	t.Helper()
	h := covCfgHandler(nil)
	block := h.buildKeeperBlock("ada", []mcpCredEntry{
		{Type: "SECRET", EnvVar: "PROD_DB_PASSWORD", Value: "v1"},
	})
	if block == "" {
		t.Fatal("expected a keeper block for a batch containing a SECRET")
	}
	return block
}

func TestKeeperBlock_NeverInterpolatesTheAgentTokenIntoArgv(t *testing.T) {
	block := keeperBlockFixture(t)

	for i, line := range strings.Split(block, "\n") {
		// Only an EXPANSION can leak. Prose may name the variable; a recipe that
		// actually carries the value has to write $NAME or ${NAME}.
		if !strings.Contains(line, "$CREWSHIP_AGENT_TOKEN") &&
			!strings.Contains(line, "${CREWSHIP_AGENT_TOKEN}") {
			continue
		}
		if keeperArgvFlag.MatchString(line) {
			t.Errorf("keeper block line %d passes the agent token as a command-line argument, "+
				"which publishes it in /proc/<pid>/cmdline to every sibling agent:\n\t%s", i+1, line)
			continue
		}
		if !keeperAuthConfigLine.MatchString(line) {
			t.Errorf("keeper block line %d mentions the agent token outside a curl -K config "+
				"directive; the only argv-free form is `header = \"Authorization: Bearer $CREWSHIP_AGENT_TOKEN\"`:\n\t%s",
				i+1, line)
		}
	}
}

// The exact regression this replaces, spelled out so a re-introduction fails
// loudly rather than sliding past the generic scan above.
func TestKeeperBlock_NoBearerHeaderFlag(t *testing.T) {
	block := keeperBlockFixture(t)

	for _, b := range []string{
		`-H "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"`,
		`-H 'Authorization: Bearer $CREWSHIP_AGENT_TOKEN'`,
		`-H "Authorization: Bearer ${CREWSHIP_AGENT_TOKEN}"`,
		`--header "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"`,
		`--oauth2-bearer $CREWSHIP_AGENT_TOKEN`,
	} {
		if strings.Contains(block, b) {
			t.Errorf("the keeper block reintroduced the argv-leaking recipe %q", b)
		}
	}
}

// keeperAuthConfigDirective is the curl config line, matched literally.
const keeperAuthConfigDirective = `header = "Authorization: Bearer $CREWSHIP_AGENT_TOKEN"`

// splitKeeperRecipes cuts the block into one segment per curl invocation, so a
// recipe's flags, heredoc and terminator all land together and nothing bleeds
// into its neighbour. Mirrors splitRecipes in
// internal/orchestrator/prompt_token_argv_test.go.
func splitKeeperRecipes(block string) []string {
	parts := strings.Split(block, "curl ")
	segments := make([]string, 0, len(parts))
	for _, p := range parts[1:] { // parts[0] is the prose before the first curl
		segments = append(segments, "curl "+p)
	}
	return segments
}

// countKeeperLines counts lines equal to want. The heredoc terminator has to be
// at column 0 — an indented `AUTH` does not end a `<<AUTH` heredoc — so a
// substring match would accept a recipe the shell will not.
func countKeeperLines(s, want string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if line == want {
			n++
		}
	}
	return n
}

// Structural check on the replacement, asserted PER RECIPE.
//
// The first version of this test counted each token across the WHOLE block, so
// the totals could be conserved while the wiring was broken: strip the redirect
// off /keeper/request, duplicate it on /keeper/execute, and the counts are
// unchanged while the first recipe 401s in production. CodeRabbit caught it and
// the mutation confirmed it. Each recipe now has to carry its own complete
// wiring — one config directive, one -K, one redirect, one terminator.
func TestKeeperBlock_AuthHeaderIsReadFromACurlConfigOnFD3(t *testing.T) {
	block := keeperBlockFixture(t)

	authenticated := 0
	for i, segment := range splitKeeperRecipes(block) {
		config := strings.Count(segment, keeperAuthConfigDirective)
		flag := strings.Count(segment, "-K /dev/fd/3")
		redirect := strings.Count(segment, "3<<AUTH")
		terminator := countKeeperLines(segment, "AUTH")

		if config+flag+redirect+terminator == 0 {
			continue // an unauthenticated call, if one is ever added
		}
		authenticated++

		if config != 1 || flag != 1 || redirect != 1 || terminator != 1 {
			t.Errorf("keeper recipe %d has incomplete fd-3 auth wiring "+
				"(config=%d, -K /dev/fd/3=%d, 3<<AUTH=%d, AUTH terminator=%d; each must be 1). "+
				"Every authenticated recipe needs its own, whatever the block-wide totals say:\n%s",
				i+1, config, flag, redirect, terminator, segment)
		}
	}

	// Pins the population: the per-recipe check above would pass a recipe whose
	// auth was deleted wholesale, since all-zero reads as "unauthenticated".
	if authenticated != 2 {
		t.Errorf("expected both keeper recipes (/keeper/request and /keeper/execute) to "+
			"authenticate via a curl config; found %d that do", authenticated)
	}
}
