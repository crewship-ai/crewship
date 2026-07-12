package gatekeeper

import "testing"

// These tests extend gatekeeper_inject_sec_test.go to the *identity* fields
// (agent / crew / credential / tool / skill / trigger names) that are rendered
// adjacent to the %q-escaped blob fields. Those name fields were emitted with
// plain %s, so a newline in an attacker-influenceable name forges prompt
// structure exactly like an unescaped blob would — a DENY→ALLOW leak against
// the aux LLM. They reuse injectionPayload / fakeInstructionLine /
// assertSnippetEscaped / secTestGatekeeper from gatekeeper_inject_sec_test.go.

func TestSecGatekeeper_AccessPromptEscapesCredentialName(t *testing.T) {
	g := secTestGatekeeper()
	req := EvalRequest{
		AgentName:      "viktor",
		CrewName:       "alpha",
		CredentialName: injectionPayload, // attacker-influenced (credential the agent names)
		SecurityLevel:  1,
	}
	req.Request.Intent = "legitimate-looking intent"
	assertSnippetEscaped(t, g.buildAccessPrompt(req))
}

func TestSecGatekeeper_AccessPromptEscapesAgentName(t *testing.T) {
	g := secTestGatekeeper()
	req := EvalRequest{
		AgentName:      injectionPayload,
		CrewName:       "alpha",
		CredentialName: "GH_TOKEN",
		SecurityLevel:  1,
	}
	req.Request.Intent = "legitimate-looking intent"
	assertSnippetEscaped(t, g.buildAccessPrompt(req))
}

func TestSecGatekeeper_BehaviorPromptEscapesToolName(t *testing.T) {
	g := secTestGatekeeper()
	req := EvalRequest{
		AgentName: "viktor",
		CrewName:  "alpha",
		Behavior: &BehaviorInput{
			ToolName:     injectionPayload, // the agent chooses which tool it invokes
			BehaviorMode: "block",
		},
	}
	assertSnippetEscaped(t, g.buildBehaviorPrompt(req))
}

func TestSecGatekeeper_BehaviorPromptEscapesRecentToolCalls(t *testing.T) {
	g := secTestGatekeeper()
	req := EvalRequest{
		AgentName: "viktor",
		CrewName:  "alpha",
		Behavior: &BehaviorInput{
			ToolName:        "bash",
			BehaviorMode:    "block",
			RecentToolCalls: []string{"ls", injectionPayload},
		},
	}
	assertSnippetEscaped(t, g.buildBehaviorPrompt(req))
}

func TestSecGatekeeper_SkillReviewPromptEscapesSkillName(t *testing.T) {
	g := secTestGatekeeper()
	req := EvalRequest{
		AgentName: "viktor",
		CrewName:  "alpha",
		SkillReview: &SkillReviewInput{
			SkillName:        injectionPayload, // agent-authored skill
			SkillDescription: "does a thing",
		},
	}
	assertSnippetEscaped(t, g.buildSkillReviewPrompt(req))
}

func TestSecGatekeeper_SkillReviewPromptEscapesDescription(t *testing.T) {
	g := secTestGatekeeper()
	req := EvalRequest{
		AgentName: "viktor",
		CrewName:  "alpha",
		SkillReview: &SkillReviewInput{
			SkillName:        "deploy",
			SkillDescription: injectionPayload, // agent-authored
		},
	}
	assertSnippetEscaped(t, g.buildSkillReviewPrompt(req))
}

func TestSecGatekeeper_NegativeLearningPromptEscapesTrigger(t *testing.T) {
	g := secTestGatekeeper()
	req := EvalRequest{
		AgentName: "viktor",
		CrewName:  "alpha",
		NegativeLesson: &NegativeLearningInput{
			TriggerKind:    injectionPayload,
			FailureSnippet: "benign",
		},
	}
	assertSnippetEscaped(t, g.buildNegativeLearningPrompt(req))
}
