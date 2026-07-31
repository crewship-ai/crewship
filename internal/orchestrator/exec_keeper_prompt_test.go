package orchestrator

import (
	"strings"
	"testing"
)

// The agent's half of the Keeper loop.
//
// Found by turning Keeper on on dev1 and asking a real agent to request a
// credential. It answered:
//
//	"I don't have a Keeper tool in my available tools. […] there is no automated
//	 Keeper that decides and responds."
//
// It was right. The prompt told it that credentials "are available as READ-ONLY
// files in /secrets/{your-slug}/" — false with Keeper on, which withholds them —
// and said nothing about the sidecar's /keeper/request or /keeper/execute. So the
// engine withheld the credential correctly and the agent had no way to ask for it:
// the whole request → judge → decision path was unreachable from the only side
// that starts it.
//
// These assertions are about the contract an agent needs, not the wording.

func TestPreamble_TellsAgentsHowToAskTheKeeper(t *testing.T) {
	p := crewshipSystemPreamble

	// The two sidecar calls, by path — a description without the endpoint is not
	// something a model can act on.
	for _, want := range []string{"/keeper/execute", "/keeper/request"} {
		if !strings.Contains(p, want) {
			t.Errorf("the preamble never mentions %s, so an agent cannot reach the Keeper", want)
		}
	}
	// The port and the auth header, or the call 401s.
	if !strings.Contains(p, "localhost:9119") {
		t.Error("the preamble does not say where the sidecar listens")
	}
	if !strings.Contains(p, "CREWSHIP_AGENT_TOKEN") {
		t.Error("the preamble does not tell the agent to authenticate the keeper call")
	}
	// The two required body fields.
	for _, want := range []string{"credential_name", "intent"} {
		if !strings.Contains(p, want) {
			t.Errorf("the preamble does not name the required field %q", want)
		}
	}
	// The three verdicts, so the agent knows what it is reading back.
	for _, want := range []string{"ALLOW", "DENY", "ESCALATE"} {
		if !strings.Contains(p, want) {
			t.Errorf("the preamble does not mention the %s verdict", want)
		}
	}
}

// A missing file under /secrets is the ONLY signal an agent has that Keeper is
// holding something. Without that sentence it reads the absence as "not
// provisioned" and gives up — which is exactly what happened on dev1.
func TestPreamble_ExplainsAWithheldCredential(t *testing.T) {
	p := strings.ToLower(crewshipSystemPreamble)

	if !strings.Contains(p, "withheld") {
		t.Error("the preamble never explains that a credential can be withheld rather than absent")
	}
	if !strings.Contains(p, "not in /secrets") && !strings.Contains(p, "not in /secrets/{your-slug}/") {
		t.Error("the preamble does not tell the agent how to notice a withheld credential")
	}
	// The claim that used to be there unconditionally, and was false with Keeper
	// on. "granted … appear as" is fine; "existing … are available" was not.
	if strings.Contains(p, "existing cli tokens and secrets are available as read-only files") {
		t.Error("the preamble still claims every granted credential is readable, which Keeper makes false")
	}
}

// The intent is the entire input to the judge, and a thin one is refused at the
// higher tiers before a model is even asked. An agent that does not know that
// retries the same four words.
func TestPreamble_SaysWhatMakesAGoodIntent(t *testing.T) {
	p := strings.ToLower(crewshipSystemPreamble)
	if !strings.Contains(p, "intent") {
		t.Fatal("the preamble does not mention the intent at all")
	}
	// It has to say the shape, not just the field name.
	for _, want := range []string{"what task", "why this credential"} {
		if !strings.Contains(p, want) {
			t.Errorf("the preamble does not tell the agent what an intent should contain (%q)", want)
		}
	}
}

// A DENY that an agent routes around is worse than no gate. The preamble has to
// close the obvious workarounds explicitly, because each of them is something a
// capable model will otherwise try in good faith.
func TestPreamble_ForbidsRoutingAroundADeny(t *testing.T) {
	p := strings.ToLower(crewshipSystemPreamble)
	for _, want := range []string{
		"do not retry", // reworded intent
		"another agent",
		"logged",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the preamble does not close the workaround %q", want)
		}
	}
}
