package notify

import (
	"strings"
	"testing"
)

// A routine's notify step has always been able to say exactly what its message
// reads like. Everything the product generates ITSELF could not: the wording of
// "Pipeline x completed" or "Scheduled routine failed: y" is computed at the
// producer and there was no way to change it.
//
// The envelope now carries the source's own facts (Vars), which is what makes
// this possible at all — a template can only reference what the message
// carries. These are the templates that turn those facts into the sentence
// somebody reads.
//
// Syntax is the house one, `{{ namespace.key }}`, so nobody has to learn a
// second: `vars.*` for the source's facts and `source.*` for what the producer
// computed, which is what lets a template ADD to a message rather than only
// replace it.

func templateMessage() CategoryMessage {
	return CategoryMessage{
		WorkspaceID: "w",
		Category:    CategoryRoutinesCompleted,
		Title:       "Pipeline nightly completed",
		Body:        "pipeline_slug: nightly",
		SourceKind:  "journal:pipeline.run.completed",
		Links:       []Link{{Label: "Open runs", Path: "/runs"}},
		Vars: map[string]any{
			"pipeline_slug":     "nightly",
			"total_duration_ms": 1200,
			"nested":            map[string]any{"crew": "ops"},
		},
	}
}

func TestRenderTemplate_ReadsTheSourcesFacts(t *testing.T) {
	got := RenderTemplate("{{ vars.pipeline_slug }} took {{ vars.total_duration_ms }}ms", templateMessage())
	if got != "nightly took 1200ms" {
		t.Errorf("got %q", got)
	}
}

func TestRenderTemplate_ReadsWhatTheProducerComputed(t *testing.T) {
	// This is what makes a template able to PREFIX rather than only replace:
	// an operator who just wants a tag keeps the producer's sentence.
	got := RenderTemplate("[{{ source.category }}] {{ source.title }}", templateMessage())
	if got != "[routines.completed] Pipeline nightly completed" {
		t.Errorf("got %q", got)
	}
}

func TestRenderTemplate_WalksOneLevelIntoAFact(t *testing.T) {
	if got := RenderTemplate("{{ vars.nested.crew }}", templateMessage()); got != "ops" {
		t.Errorf("got %q", got)
	}
}

func TestRenderTemplate_MissingReferencesRenderEmpty(t *testing.T) {
	// Same contract as an absent routine input: a template that names
	// something this event does not carry loses that fragment rather than
	// failing the delivery. A notification that arrives with a gap beats one
	// that does not arrive.
	for _, tmpl := range []string{
		"{{ vars.nope }}", "{{ source.nope }}", "{{ nope.key }}", "{{ }}", "{{ bare }}",
	} {
		if got := RenderTemplate("x"+tmpl+"y", templateMessage()); got != "xy" {
			t.Errorf("template %q rendered %q, want the reference dropped", tmpl, got)
		}
	}
}

func TestRenderTemplate_LeavesTextWithNoReferencesAlone(t *testing.T) {
	const plain = "A plain sentence with no placeholders."
	if got := RenderTemplate(plain, templateMessage()); got != plain {
		t.Errorf("got %q", got)
	}
}

// Apply is where "no template configured" has to mean "nothing changes" —
// every category ships without one, so this is the path almost every
// notification takes.

func TestApply_AnEmptyTemplateChangesNothing(t *testing.T) {
	msg := templateMessage()
	out := MessageTemplate{Category: CategoryRoutinesCompleted}.Apply(msg)
	if out.Title != msg.Title || out.Body != msg.Body {
		t.Errorf("an unset template must leave the message alone: %+v", out)
	}
}

func TestApply_OnlyTheFieldTheTemplateSetsIsReplaced(t *testing.T) {
	// An operator who wants a different title should not have to restate the
	// body to keep it.
	msg := templateMessage()
	out := MessageTemplate{
		Category: CategoryRoutinesCompleted,
		Title:    "{{ vars.pipeline_slug }} finished",
	}.Apply(msg)

	if out.Title != "nightly finished" {
		t.Errorf("title = %q", out.Title)
	}
	if out.Body != msg.Body {
		t.Errorf("body should be untouched, got %q", out.Body)
	}
}

func TestApply_DoesNotDisturbTheRestOfTheEnvelope(t *testing.T) {
	// Links, category, priority and the facts themselves are not the
	// template's business — it writes the words, not the routing.
	msg := templateMessage()
	out := MessageTemplate{Category: CategoryRoutinesCompleted, Title: "x", Body: "y"}.Apply(msg)

	if len(out.Links) != 1 || out.Links[0].Path != "/runs" {
		t.Errorf("links changed: %+v", out.Links)
	}
	if out.Category != msg.Category || out.SourceKind != msg.SourceKind {
		t.Errorf("routing fields changed: %+v", out)
	}
	if out.Vars["pipeline_slug"] != "nightly" {
		t.Errorf("facts changed: %+v", out.Vars)
	}
}

func TestApply_ATemplateThatRendersToNothingKeepsTheOriginal(t *testing.T) {
	// A title template referencing a fact this event lacks would otherwise
	// produce an EMPTY title — a notification with no subject line, which on
	// a push service is a blank lock-screen entry. Falling back to what the
	// producer computed is the only useful answer.
	msg := templateMessage()
	out := MessageTemplate{
		Category: CategoryRoutinesCompleted,
		Title:    "{{ vars.absent }}",
	}.Apply(msg)

	if out.Title != msg.Title {
		t.Errorf("title = %q, want the producer's title back", out.Title)
	}
}

func TestApply_WhitespaceOnlyResultCountsAsNothing(t *testing.T) {
	msg := templateMessage()
	out := MessageTemplate{Category: CategoryRoutinesCompleted, Title: "  {{ vars.absent }}  "}.Apply(msg)
	if out.Title != msg.Title {
		t.Errorf("title = %q, want the producer's title back", out.Title)
	}
}

func TestApply_RenderedValuesAreStillScrubbedAtDelivery(t *testing.T) {
	// A template can pull any fact into the title, including one holding a
	// secret. That must not become a way around the redaction — delivery
	// scrubs the whole envelope, and applying a template BEFORE delivery is
	// what keeps that true.
	msg := templateMessage()
	msg.Vars["token"] = testSecret
	out := MessageTemplate{Category: CategoryRoutinesCompleted, Title: "key {{ vars.token }}"}.Apply(msg)

	if !strings.Contains(out.Title, testSecret) {
		t.Fatalf("precondition: the template should have pulled the value in, got %q", out.Title)
	}
	scrubMessage(&out, func(s string) string { return strings.ReplaceAll(s, testSecret, "[REDACTED]") })
	if strings.Contains(out.Title, testSecret) {
		t.Errorf("a templated title escaped redaction: %q", out.Title)
	}
}
