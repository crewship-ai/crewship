package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// The judge never sees a credential's VALUE — the prompt carries its name and
// tier, and that is the whole point: it judges behaviour, not secrets.
//
// The conversation history was the hole. It went into the prompt verbatim, and
// agents put secrets in conversations: they echo an environment variable, paste
// a token into a command, print a config file while debugging. That history then
// travelled to the model AND was stored in keeper_requests.ollama_prompt, so a
// secret an agent mentioned once was durably recorded next to the request that
// mentioned it.
//
// Scrubbed at LOAD, in the one place both credential paths go through, so the
// prompt, the model call and the audit row are clean by construction rather
// than by three callers each remembering.

func TestScrubJudgeText_RemovesSecretsFromConversationHistory(t *testing.T) {
	cases := []struct {
		name  string
		input string
		leak  string
	}{
		{
			"aws access key",
			"assistant: I exported AKIAIOSFODNN7EXAMPLE before running terraform",
			"AKIAIOSFODNN7EXAMPLE",
		},
		{
			// Fabricated, and shaped like the real thing on purpose: a fixture the
			// scanner would ignore is one the SCRUBBER would ignore too, which
			// would leave this test asserting nothing. gitleaks blocking the commit
			// is the gate working — these lines are the exception it is built for.
			"bearer token",
			"assistant: curl -H 'Authorization: Bearer sk-proj-abc123def456ghi789jkl012mno345pqr' https://api.example.com", //gitleaks:allow
			"sk-proj-abc123def456ghi789jkl012mno345pqr", //gitleaks:allow
		},
		{
			"github token",
			"assistant: the deploy used ghp_16C7e42F292c6912E7710c838347Ae178B4a and it worked", //gitleaks:allow
			"ghp_16C7e42F292c6912E7710c838347Ae178B4a",                                          //gitleaks:allow
		},
		{
			"private key block",
			"assistant: the key is -----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----",
			"MIIEowIBAAKCAQEA",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubJudgeText(tc.input)
			if strings.Contains(got, tc.leak) {
				t.Errorf("secret survived into the judge prompt:\n%s", got)
			}
			if got == "" {
				t.Error("the whole history was dropped — the judge needs the context, just not the secret")
			}
		})
	}
}

// Ordinary conversation must come through intact. A scrubber that mangles the
// history makes the judge worse at the job it is there to do, and the decision
// criteria explicitly ask it to weigh whether the conversation supports the
// request.
func TestScrubJudgeText_LeavesOrdinaryConversationAlone(t *testing.T) {
	in := "user: the staging certs expire Friday\n" +
		"assistant: I'll rotate them on staging-web-01 and reload nginx\n" +
		"assistant: confirmed, the cert expires in 3 days"
	if got := scrubJudgeText(in); got != in {
		t.Errorf("ordinary conversation was altered:\nwant %q\ngot  %q", in, got)
	}
}

// The intent is agent-authored too, and travels the same way.
func TestScrubJudgeText_HandlesEmptyAndPlainInput(t *testing.T) {
	if got := scrubJudgeText(""); got != "" {
		t.Errorf("empty input became %q", got)
	}
	if got := scrubJudgeText("rotate the staging certificates"); got != "rotate the staging certificates" {
		t.Errorf("plain intent was altered: %q", got)
	}
}

// The intent is agent-authored, exactly like the conversation history, and an
// agent that pastes a token into a chat will paste one into a justification:
// "publish the release with ghp_… since the CI token expired".
//
// Scrubbing it is not enough on its own — it has to happen BEFORE anything reads
// it. keeper.Request captures Intent by value into `req`, and `req` is what
// gatekeeper.EvalRequest carries into buildAccessPrompt. Scrubbing body.Intent
// after `req` was built therefore sanitises a copy nobody uses: the judge still
// receives the raw string, keeper_requests.intent still stores it, and the
// keeper.request journal payload still carries it.
//
// That is a scrub that passes review by existing and protects nothing, so this
// asserts the three places the raw value would otherwise land rather than
// asserting that the function was called.
func TestKeeperRequest_ScrubsTheIntentBeforeAnythingReadsIt(t *testing.T) {
	const leak = "ghp_16C7e42F292c6912E7710c838347Ae178B4a" //gitleaks:allow — fabricated, shaped like the real thing so the scrubber's pattern actually engages

	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &capturingEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionAllow), RiskScore: 2,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	rr := doKeeperRequest(h, keeperRequestBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "publish the release using " + leak + " because the CI token expired",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("request returned %d: %s", rr.Code, rr.Body.String())
	}

	// 1. What the judge was handed. This is the one that leaves the machine when
	//    the judge is hosted, and the reason the operator asked for it at all.
	if strings.Contains(gk.seen.Request.Intent, leak) {
		t.Errorf("the judge received the raw intent:\n%s", gk.seen.Request.Intent)
	}
	if gk.seen.Request.Intent == "" {
		t.Error("the intent was emptied — the judge is asked to weigh it, so it must survive scrubbed, not vanish")
	}

	// 2. What the audit row stores. keeper_requests is long-lived and readable by
	//    anyone who can read a keeper request; a secret recorded here outlives the
	//    conversation that mentioned it.
	var storedIntent string
	if err := db.QueryRow(`SELECT intent FROM keeper_requests WHERE requesting_agent_id = ?`,
		agentID).Scan(&storedIntent); err != nil {
		t.Fatalf("read stored intent: %v", err)
	}
	if strings.Contains(storedIntent, leak) {
		t.Errorf("keeper_requests.intent stored the raw secret:\n%s", storedIntent)
	}

	// The keeper.request journal payload is the third site that reads the intent,
	// and the Timeline shows it to anyone who can see the crew — a wider audience
	// than the escalation is addressed to. It is not asserted here because
	// NewKeeperHandler wires a noopEmitter and the real Writer is async, which
	// would buy a third assertion at the cost of a flush-timing flake. The two
	// above are sufficient: all three read the same body.Intent, so a scrub early
	// enough for the judge is early enough for the journal, and one late enough
	// to miss the journal would have failed the first assertion first.
}

// The /execute path carries the same agent-authored intent and must scrub it the
// same way — but it must NOT scrub the command, which is a distinction worth a
// test rather than a comment. The command is run verbatim (`sh -c`), so a
// scrubber that rewrote a span it mistook for a token would run something other
// than what was judged; and the command is precisely what the judge is asked to
// inspect, so redacting it would defeat the evaluation.
func TestKeeperExecute_ScrubsTheIntentButNeverTheCommand(t *testing.T) {
	const leak = "ghp_16C7e42F292c6912E7710c838347Ae178B4a" //gitleaks:allow — fabricated; see the access-path test

	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	gk := &capturingEvaluator{resp: keeper.GatekeeperResponse{
		Decision: string(keeper.DecisionDeny), RiskScore: 9,
	}}
	h := newKeeperHandlerWithGK(t, db, gk)

	command := "curl -H 'Authorization: Bearer " + leak + "' https://api.example.com" //gitleaks:allow
	raw, _ := json.Marshal(keeperExecuteBody{
		RequestingAgentID: agentID,
		RequestingCrewID:  crewID,
		WorkspaceID:       wsID,
		CredentialID:      credID,
		Intent:            "call the API using " + leak + " while the vault entry is rotated",
		Command:           command,
		ContainerID:       "container-1",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/execute", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	h.HandleExecute(httptest.NewRecorder(), req)

	if !gk.called {
		t.Fatal("the judge was never asked, so this test proves nothing about what it saw")
	}
	if strings.Contains(gk.seen.Request.Intent, leak) {
		t.Errorf("the judge received the raw intent:\n%s", gk.seen.Request.Intent)
	}
	if gk.seen.Command != command {
		t.Errorf("the command was altered before the judge saw it.\nwant %q\ngot  %q\n"+
			"a redacted command cannot be judged, and this one is executed verbatim",
			command, gk.seen.Command)
	}
}
