package pages

import (
	"strings"
	"testing"
)

// §8 rule 3 keeps URLs out of agent prose. It must not keep PROSE out of agent
// prose, and a bare substring match does exactly that: a scheme name is only a
// scheme when something separates it from the word in front of it.
//
// `narrative.v1` is the panel an agent writes, and the refusal is a hard 400.
// So an incident summary reading "Profile: 42 active users" was refused for
// carrying "file:" — as was any sentence containing Metadata:, Makefile:,
// Hotel: or a hundred other ordinary words ending in a scheme name.
func TestNarrative_AWordEndingInASchemeNameIsNotAURL(t *testing.T) {
	t.Parallel()

	prose := []string{
		"Profile: 42 active users",      // file:
		"Metadata: 3 tables scanned",    // data:
		"Makefile: target build failed", // file:
		"Hotel: 5 free rooms",           // tel:
		"Intel: the queue drained at 04:12",
		"Overall: nothing needs attention",
	}
	for _, text := range prose {
		body := `{"blocks":[{"kind":"paragraph","text":"` + text + `"}]}`
		if _, err := ValidatePayload(SchemaNarrative, []byte(body)); err != nil {
			t.Errorf("refused ordinary prose %q: %v", text, err)
		}
	}
}

// The rule it exists for still holds. Each of these is a scheme a client would
// act on, at a real boundary.
func TestNarrative_StillRefusesAURL(t *testing.T) {
	t.Parallel()

	urls := []string{
		"see https://evil.example.com/x",
		"data:text/html;base64,PHNjcmlwdD4=",
		"click javascript:alert(1)",
		"open file:///etc/passwd",
		"mail me at mailto:a@b.c",
		"call tel:+420123456789",
		"go to //evil.example.com",
		"visit www.evil.example.com",
		"blob:https://x/y",
		"Report data:text/plain,leak", // a scheme after a space is still a scheme
	}
	for _, text := range urls {
		body := `{"blocks":[{"kind":"paragraph","text":"` + text + `"}]}`
		if _, err := ValidatePayload(SchemaNarrative, []byte(body)); err == nil {
			t.Errorf("accepted a URL: %q", text)
		}
	}
}

// Every published schema declares `$schema` and tells producers to send it —
// "Present so a hand-written payload gets inline validation in an editor". The
// JSON-Schema pass accepts it and the strict decoder then refused it, so a
// payload written the way our own documentation describes was rejected by the
// validator that documentation belongs to.
func TestPayload_AcceptsTheSchemaKeyItsOwnSchemasAdvertise(t *testing.T) {
	t.Parallel()

	cases := map[PanelSchema]string{
		SchemaMetric:    `{"$schema":"https://crewship.ai/schemas/panel.metric.v1.json","value":1,"unit":"ms"}`,
		SchemaStatus:    `{"$schema":"x","items":[{"name":"api","state":"ok"}]}`,
		SchemaTable:     `{"$schema":"x","columns":[{"key":"a","label":"A"}],"rows":[{"a":1}]}`,
		SchemaNarrative: `{"$schema":"x","blocks":[{"kind":"paragraph","text":"ok"}]}`,
		SchemaSeries:    `{"$schema":"x","unit":"ms","labels":["a"],"series":[{"name":"s","values":[1]}]}`,
	}
	for schema, body := range cases {
		if _, err := ValidatePayload(schema, []byte(body)); err != nil {
			t.Errorf("%s refused its own advertised $schema key: %v", schema, err)
		}
	}
}

// $schema is ignored, not stored: it must not reach the payload a panel
// renders, and an unknown field that is NOT $schema must still be refused.
func TestPayload_StillRefusesAnUnknownField(t *testing.T) {
	t.Parallel()

	_, err := ValidatePayload(SchemaMetric, []byte(`{"value":1,"unit":"ms","totally_made_up":true}`))
	if err == nil {
		t.Fatal("an unknown field was accepted")
	}
	if !strings.Contains(err.Error(), "totally_made_up") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}
