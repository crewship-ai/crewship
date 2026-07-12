package main

// Unit tests for `crewship routine iterate` pure helpers: grader-score
// parsing, optimizer definition extraction, and prompt construction.
// These run without network — the RunE loop is a thin orchestration
// over these functions plus the existing run/save/validate calls.

import (
	"strings"
	"testing"
)

func TestParseGraderScore_PlainJSON(t *testing.T) {
	got, err := parseGraderScore(`{"score": 74, "feedback": "output misses the summary section"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Score != 74 {
		t.Errorf("score: got %d, want 74", got.Score)
	}
	if got.Feedback != "output misses the summary section" {
		t.Errorf("feedback: got %q", got.Feedback)
	}
}

func TestParseGraderScore_FencedAndProse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"fenced", "Here is my assessment:\n```json\n{\"score\": 88, \"feedback\": \"good\"}\n```\nDone.", 88},
		{"prose-wrapped", "I evaluated the run.\n{\"score\": 42, \"feedback\": \"weak\"}\nThat is all.", 42},
		{"float-score", `{"score": 66.7, "feedback": "ok"}`, 66},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGraderScore(tc.in)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Score != tc.want {
				t.Errorf("score: got %d, want %d", got.Score, tc.want)
			}
		})
	}
}

func TestParseGraderScore_ClampsRange(t *testing.T) {
	over, err := parseGraderScore(`{"score": 250, "feedback": ""}`)
	if err != nil {
		t.Fatalf("parse over: %v", err)
	}
	if over.Score != 100 {
		t.Errorf("over-range should clamp to 100, got %d", over.Score)
	}
	under, err := parseGraderScore(`{"score": -3, "feedback": ""}`)
	if err != nil {
		t.Fatalf("parse under: %v", err)
	}
	if under.Score != 0 {
		t.Errorf("under-range should clamp to 0, got %d", under.Score)
	}
}

func TestParseGraderScore_MissingScore_Errors(t *testing.T) {
	if _, err := parseGraderScore(`{"feedback": "no score field"}`); err == nil {
		t.Error("expected error when score field is absent")
	}
	if _, err := parseGraderScore("no json here at all"); err == nil {
		t.Error("expected error on non-JSON grader output")
	}
}

func TestExtractDefinitionJSON_Fenced(t *testing.T) {
	in := "Improved definition below.\n```json\n{\"name\": \"summarize\", \"steps\": [{\"id\": \"s1\"}]}\n```\nApplied fixes: tightened prompt."
	got, err := extractDefinitionJSON(in)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(string(got), `"name": "summarize"`) {
		t.Errorf("extracted wrong block: %s", got)
	}
}

func TestExtractDefinitionJSON_RawObject(t *testing.T) {
	in := `{"name": "summarize", "steps": []}`
	got, err := extractDefinitionJSON(in)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if strings.TrimSpace(string(got)) != in {
		t.Errorf("raw object should round-trip, got %s", got)
	}
}

func TestExtractDefinitionJSON_ProseWrappedObject(t *testing.T) {
	in := "Sure — here is the updated routine:\n{\"name\": \"x\", \"steps\": [{\"id\": \"a\", \"nested\": {\"k\": \"v\"}}]}\nLet me know."
	got, err := extractDefinitionJSON(in)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(string(got), `"nested"`) {
		t.Errorf("should capture the full nested object, got %s", got)
	}
}

func TestExtractDefinitionJSON_Invalid_Errors(t *testing.T) {
	for _, in := range []string{
		"no definition here",
		"```json\n{broken\n```",
		"",
	} {
		if _, err := extractDefinitionJSON(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestBuildGradePrompt_ContainsPieces(t *testing.T) {
	p := buildGradePrompt("Rubric: output must contain a TL;DR.", `{"text":"..."}`, "Long output body", "")
	for _, want := range []string{"TL;DR", "Long output body", `"score"`, "0-100"} {
		if !strings.Contains(p, want) {
			t.Errorf("grade prompt missing %q", want)
		}
	}
}

func TestBuildGradePrompt_FailedRunIncludesError(t *testing.T) {
	p := buildGradePrompt("rubric", "{}", "", "step s2: agent timed out")
	if !strings.Contains(p, "agent timed out") {
		t.Error("grade prompt for a failed run must include the error message")
	}
}

func TestBuildOptimizePrompt_ContainsPieces(t *testing.T) {
	def := []byte(`{"name":"summarize","steps":[]}`)
	p := buildOptimizePrompt(def, "rubric text", iterateScore{Score: 61, Feedback: "misses TL;DR"}, "run output", "")
	for _, want := range []string{`"name":"summarize"`, "rubric text", "61", "misses TL;DR", "ONLY the complete improved JSON"} {
		if !strings.Contains(p, want) {
			t.Errorf("optimize prompt missing %q", want)
		}
	}
}

func TestIterateChangeSummary_Format(t *testing.T) {
	got := iterateChangeSummary(2, iterateScore{Score: 74, Feedback: "misses the summary section and the tone is off"})
	if !strings.HasPrefix(got, "iterate round 2: score 74/100") {
		t.Errorf("summary prefix wrong: %q", got)
	}
	if len(got) > 160 {
		t.Errorf("summary should stay one-line short (<=160 chars), got %d", len(got))
	}
}
