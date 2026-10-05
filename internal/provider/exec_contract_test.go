package provider_test

import (
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestExecUserRequiresPositiveNumericUIDAndGID(t *testing.T) {
	for _, user := range []string{"", "root", "toor", "0", "0:1001", "1001:0", "-1", "1001:-2", "1001:", ":1001", "1001:1001:1001", "99999999999999999999999999999"} {
		if !provider.IsPrivilegedExecUser(user) {
			t.Errorf("unsafe or unprovable exec identity %q accepted", user)
		}
	}
	for _, user := range []string{"1001", "1001:1001", " 1001:1002 ", "65534:65534"} {
		if provider.IsPrivilegedExecUser(user) {
			t.Errorf("non-root numeric identity %q refused", user)
		}
	}
}

func TestRunTerminationCommandsPreserveExactSessionAndProcessGroups(t *testing.T) {
	// An argv element must remain one element even when it contains shell
	// metacharacters. No shell expansion or slug-only targeting is permitted.
	session := "agent-worker-run-owned; echo unrelated"
	for _, tc := range []struct{ got, want []string }{
		{provider.KillSignalCmd("TERM", 4321), []string{"kill", "-TERM", "4321"}},
		{provider.TmuxKillSessionCmd(session), []string{"tmux", "kill-session", "-t", session}},
		{provider.TmuxListSessionsCmd(), []string{"tmux", "list-sessions", "-F", "#{session_name}"}},
		{provider.TmuxListPanePIDsCmd(session), []string{"tmux", "list-panes", "-t", session, "-F", "#{pane_pid}"}},
		{provider.KillProcessGroupCmd("KILL", []int{4321, 8765}), []string{"kill", "-KILL", "--", "-4321", "-8765"}},
	} {
		if !reflect.DeepEqual(tc.got, tc.want) {
			t.Errorf("termination command = %q, want %q", tc.got, tc.want)
		}
	}
}

func TestContainerDisplayIDsAndCrewNetworksKeepIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{"", ""}, {"short", "short"}, {"123456789012", "123456789012"}, {"1234567890123456", "123456789012"}} {
		if got := provider.ShortID(tc.raw); got != tc.want {
			t.Errorf("ShortID(%q) = %q", tc.raw, got)
		}
	}
	if got := provider.CrewNetworkName("", "id-one"); got != "crewship-crew-id-one" {
		t.Fatalf("default network = %q", got)
	}
	if got := provider.CrewNetworkName("instance-3", "id-one"); got != "instance-3-crew-id-one" {
		t.Fatalf("scoped network = %q", got)
	}
}
