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
		{ID: "cnativecredential000000001", Type: "SECRET", EnvVar: "PROD_DB_PASSWORD", Value: "v1"},
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

// Native CLI owns the bearer header. Prompt recipes must never ask the shell
// to expand it, even when a future example adds a different authenticated call.
func TestKeeperBlock_UsesNativeCLIWithoutTokenExpansion(t *testing.T) {
	block := keeperBlockFixture(t)
	for _, command := range []string{"crewship keeper request ", "crewship keeper execute ", "crewship keeper request-status "} {
		if !strings.Contains(block, command) {
			t.Errorf("missing native recipe %q", command)
		}
	}
	for _, unsafe := range []string{"curl ", "$CREWSHIP_AGENT_TOKEN", "${CREWSHIP_AGENT_TOKEN}", "Authorization:"} {
		if strings.Contains(block, unsafe) {
			t.Errorf("native Keeper prompt delegates authentication material to shell: %q", unsafe)
		}
	}
	if !strings.Contains(block, "--command-stdin") {
		t.Error("execute must read command through stdin")
	}
	if !strings.Contains(block, "--id") || !strings.Contains(block, "cnativecredential000000001") {
		t.Error("native recipes must use the assigned credential identity")
	}
}
