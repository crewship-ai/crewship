package api

import (
	"strings"
	"testing"
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
