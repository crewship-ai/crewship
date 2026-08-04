package orchestrator

// Delegation for non-lead agents (#1754).
//
// Before this, "only leads delegate" was enforced by not telling anyone else
// the endpoint existed: the /assign recipe lives in leadContextStaticTail,
// appended for AgentRole == "LEAD". The server never checked. Unlocking it is
// therefore two things — say it in the prompt, and hand a worker run the
// per-agent token its call has to carry — and both are only safe because the
// caps in internal/api/delegation_limits.go landed first.

import (
	"context"
	"strings"
	"testing"
)

func TestPeerContext_TellsANonLeadHowToDelegate(t *testing.T) {
	out := BuildPeerContext([]CrewMember{
		{Name: "Ada", Slug: "ada", RoleTitle: "engineer"},
		{Name: "Bo", Slug: "bo"},
	}, "cy")

	if !strings.Contains(out, "9119/assign") {
		t.Error("a non-lead agent is never told /assign exists, so the endpoint stays a secret rather than a permission")
	}
	if !strings.Contains(out, "9119/results/") {
		t.Error("delegation without a way to read the result is a fire-and-forget hand-off")
	}
}

// The prompt has to be honest about the cap. An agent that will be refused
// mid-task and does not know a limit exists reports "Crewship is broken";
// one that knows reports "I hit the delegation depth limit".
func TestPeerContext_NamesTheDelegationCap(t *testing.T) {
	out := BuildPeerContext([]CrewMember{{Name: "Ada", Slug: "ada"}}, "cy")
	for _, want := range []string{"depth", "fan-out", "refused"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("peer context must warn about %q — the cap is enforced server-side and the agent meets it as a 403", want)
		}
	}
}

// Same obligation on the lead side: the recipe it has always had now has a
// limit behind it.
func TestLeadContext_NamesTheDelegationCap(t *testing.T) {
	out := BuildLeadContext([]CrewMember{{Name: "Ada", Slug: "ada"}}, nil)
	if !strings.Contains(strings.ToLower(out), "fan-out") {
		t.Error("lead context does not mention the fan-out limit its /assign recipe now runs into")
	}
}

// A worker sub-agent runs with SkipSidecar — it does not START a sidecar,
// because the crew's is already listening on 127.0.0.1:9119 in the same
// container. It was, however, also given no per-agent token, so any call it
// made to that sidecar arrived anonymous. With /assign now resolving identity,
// anonymous means 403, and delegation would be unlocked in the prompt and shut
// in practice.
func TestSkipSidecarRun_StillGetsItsOwnAgentToken(t *testing.T) {
	c := covNewRunContainer(covRunOpts{stream: "{}\n"})
	o := New(c, newMemState(), covQuietLogger())
	o.SetIPCConfig("http://crewshipd:8080", "master-internal-token")

	req := covRunReq()
	req.SkipSidecar = true
	req.CrewMembers = []CrewMember{{ID: "m1", Slug: "ada", Name: "Ada"}}
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	want := "CREWSHIP_AGENT_TOKEN=" + agentAuthToken("master-internal-token", req.WorkspaceID, req.AgentID, covQuietLogger())
	if want == "CREWSHIP_AGENT_TOKEN=" {
		t.Fatal("fixture derived an empty token; the assertion below would be vacuous")
	}
	for _, call := range c.snapshotCalls() {
		if len(call.Cmd) == 0 || call.Cmd[0] != "stdbuf" {
			continue
		}
		for _, e := range call.Env {
			if e == want {
				return
			}
		}
		t.Fatalf("worker exec env carries no per-agent token; it cannot authenticate to the crew's sidecar. env=%v", call.Env)
	}
	t.Fatal("agent exec not captured")
}
