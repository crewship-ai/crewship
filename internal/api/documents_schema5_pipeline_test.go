package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

// TestDocumentRichTreeSchemaFive (#5233): the Go proof validator knows the
// schema-5 marks and atoms exactly as the engine does, and nothing more.
func TestDocumentRichTreeSchemaFive(t *testing.T) {
	paragraph := func(content string) string {
		return `{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"p1"},"content":[` + content + `]}]}`
	}
	marked := func(marks string) string { return paragraph(`{"type":"text","text":"x","marks":` + marks + `}`) }
	for _, tt := range []struct {
		name   string
		raw    string
		schema int
		ok     bool
	}{
		{"underline", marked(`[{"type":"underline","attrs":{}}]`), 5, true},
		{"underline on 4", marked(`[{"type":"underline","attrs":{}}]`), 4, false},
		{"palette colour and highlight", marked(`[{"type":"bold","attrs":{}},{"type":"textColor","attrs":{"color":"red"}},{"type":"highlight","attrs":{"color":"yellow"}}]`), 5, true},
		{"every combinable mark", marked(`[{"type":"link","attrs":{"href":"https://example.com/"}},{"type":"bold"},{"type":"italic"},{"type":"strike"},{"type":"underline"},{"type":"textColor","attrs":{"color":"blue"}},{"type":"highlight","attrs":{"color":"pink"}}]`), 5, true},
		{"CSS colour", marked(`[{"type":"textColor","attrs":{"color":"#ff0000"}}]`), 5, false},
		{"unknown palette name", marked(`[{"type":"highlight","attrs":{"color":"teal"}}]`), 5, false},
		{"colour without attrs", marked(`[{"type":"textColor"}]`), 5, false},
		{"colour with a style", marked(`[{"type":"textColor","attrs":{"color":"red","style":"x"}}]`), 5, false},
		{"underline with attrs", marked(`[{"type":"underline","attrs":{"color":"red"}}]`), 5, false},
		{"code with underline", marked(`[{"type":"code"},{"type":"underline"}]`), 5, false},
		{"colour on 4", marked(`[{"type":"textColor","attrs":{"color":"red"}}]`), 4, false},
		{"date and mention", paragraph(`{"type":"text","text":"due "},{"type":"date","attrs":{"value":"2028-02-29"}},{"type":"mention","attrs":{"userId":"user_1"}}`), 5, true},
		{"date on 4", paragraph(`{"type":"date","attrs":{"value":"2026-11-20"}}`), 4, false},
		{"not a calendar day", paragraph(`{"type":"date","attrs":{"value":"2026-02-29"}}`), 5, false},
		{"year zero", paragraph(`{"type":"date","attrs":{"value":"0000-01-01"}}`), 5, false},
		{"date with time", paragraph(`{"type":"date","attrs":{"value":"2026-11-20T10:00"}}`), 5, false},
		{"mention with a name", paragraph(`{"type":"mention","attrs":{"userId":"u1","label":"Ann"}}`), 5, false},
		{"mention id with markup", paragraph(`{"type":"mention","attrs":{"userId":"<b>"}}`), 5, false},
		{"atom with marks", paragraph(`{"type":"date","attrs":{"value":"2026-11-20"},"marks":[{"type":"bold"}]}`), 5, false},
		{"atom with content", paragraph(`{"type":"date","attrs":{"value":"2026-11-20"},"content":[]}`), 5, false},
		{"atom with an id", paragraph(`{"type":"mention","attrs":{"userId":"u1","id":"m1"}}`), 5, false},
		{"atom in a code block", `{"type":"doc","content":[{"type":"codeBlock","attrs":{"id":"c1"},"content":[{"type":"date","attrs":{"value":"2026-11-20"}}]}]}`, 5, false},
		{"atom as a block", `{"type":"doc","content":[{"type":"date","attrs":{"value":"2026-11-20"}}]}`, 5, false},
		{"schema 6", marked(`[{"type":"bold"}]`), 6, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := documentRichTreeFor(json.RawMessage(tt.raw), tt.schema); (err == nil) != tt.ok {
				t.Fatalf("err=%v want ok=%v", err, tt.ok)
			}
		})
	}
}

// TestDocumentRichSemanticGuardSchemaFive (#5233): the semantic proof treats a
// date or mention as zero-width text, keeps every mark and atom outside the
// edit, and refuses an edit whose atom side would be a guess.
func TestDocumentRichSemanticGuardSchemaFive(t *testing.T) {
	date := map[string]any{"type": "date", "attrs": map[string]any{"value": "2026-11-20"}}
	mention := map[string]any{"type": "mention", "attrs": map[string]any{"userId": "u1"}}
	text := func(value string, marks ...map[string]any) map[string]any {
		node := map[string]any{"type": "text", "text": value}
		if len(marks) > 0 {
			list := []any{}
			for _, m := range marks {
				list = append(list, m)
			}
			node["marks"] = list
		}
		return node
	}
	red := map[string]any{"type": "textColor", "attrs": map[string]any{"color": "red"}}
	under := map[string]any{"type": "underline", "attrs": map[string]any{}}
	tree := func(parts ...map[string]any) json.RawMessage {
		content := []any{}
		for _, part := range parts {
			content = append(content, part)
		}
		raw, err := json.Marshal(map[string]any{"type": "doc", "content": []any{map[string]any{"type": "paragraph", "attrs": map[string]any{"id": "p"}, "content": content}}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// "Ask " [red "Ann"] " by " <date> " and " <mention> " now." -> text "Ask Ann by  and  now."
	before := tree(text("Ask "), text("Ann", red), text(" by "), date, text(" and "), mention, text(" now.", under))
	for _, tt := range []struct {
		name  string
		edit  documentSemanticEdit
		text  string
		after json.RawMessage
		ok    bool
	}{
		{"edit away from atoms keeps marks and atoms", documentSemanticEdit{From: 0, To: 3, Insert: "Tell"}, "Tell Ann by  and  now.",
			tree(text("Tell "), text("Ann", red), text(" by "), date, text(" and "), mention, text(" now.", under)), true},
		{"edit inside marked text after the mention", documentSemanticEdit{From: 17, To: 20, Insert: "today"}, "Ask Ann by  and  today.",
			tree(text("Ask "), text("Ann", red), text(" by "), date, text(" and "), mention, text(" ", under), text("today"), text(".", under)), true},
		{"delete right before the date", documentSemanticEdit{From: 10, To: 11}, "Ask Ann by and  now.",
			tree(text("Ask "), text("Ann", red), text(" by"), date, text(" and "), mention, text(" now.", under)), true},
		{"insert beside the date", documentSemanticEdit{From: 11, To: 11, Insert: "X"}, "Ask Ann by X and  now.",
			tree(text("Ask "), text("Ann", red), text(" by "), date, text("X and "), mention, text(" now.", under)), false},
		{"delete across the date", documentSemanticEdit{From: 10, To: 12}, "Ask Ann byand  now.",
			tree(text("Ask "), text("Ann", red), text(" by"), date, text("and "), mention, text(" now.", under)), false},
		{"a dropped mention", documentSemanticEdit{From: 0, To: 3, Insert: "Tell"}, "Tell Ann by  and  now.",
			tree(text("Tell "), text("Ann", red), text(" by "), date, text(" and "), text(" now.", under)), false},
		{"a dropped colour", documentSemanticEdit{From: 0, To: 3, Insert: "Tell"}, "Tell Ann by  and  now.",
			tree(text("Tell Ann by "), date, text(" and "), mention, text(" now.", under)), false},
		{"a moved date", documentSemanticEdit{From: 0, To: 3, Insert: "Tell"}, "Tell Ann by  and  now.",
			tree(text("Tell "), text("Ann", red), text(" by  and "), date, mention, text(" now.", under)), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first, err := documentRichTreeFor(before, 5)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := documentRichTreeFor(tt.after, 5); err != nil {
				t.Fatal(err)
			}
			last, _ := documentRichTreeFor(tt.after, 5)
			edit := tt.edit
			semantic := &documentSemanticPatch{Target: "p", Text: tt.text, Edit: &edit, Rich: &documentRichProof{Before: before, After: tt.after}}
			hashes := map[string]string{"p": documentRichHash(documentRichFind(first, "p"))}
			state := documents.State{Schema: 5, Projection: richTestProjection(t, first)}
			err = documentRichSemanticGuardFor(5, semantic, hashes, richTestProjection(t, last), nil)(context.Background(), nil, "agent", state)
			if (err == nil) != tt.ok {
				t.Fatalf("err=%v want ok=%v", err, tt.ok)
			}
			// The same proof never passes against a Doc of another schema.
			state.Schema = 4
			if err := documentRichSemanticGuardFor(5, semantic, hashes, richTestProjection(t, last), nil)(context.Background(), nil, "agent", state); !errors.Is(err, documents.ErrSchema) {
				t.Fatalf("schema-4 Doc: %v", err)
			}
		})
	}
}

// TestDocumentProposalInputBindsSchema (#5233): the stored proposal carries
// the schema its proof was validated at; schemas without proposals refuse.
func TestDocumentProposalInputBindsSchema(t *testing.T) {
	raw := json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"p"},"content":[{"type":"text","text":"Hi "},{"type":"mention","attrs":{"userId":"u1"}}]}]}`)
	next := json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"p"},"content":[{"type":"text","text":"Hello "},{"type":"mention","attrs":{"userId":"u1"}}]}]}`)
	tree, _ := documentRichTreeFor(raw, 5)
	hash := documentRichHash(documentRichFind(tree, "p"))
	generation := int64(0)
	semantic := &documentSemanticPatch{Target: "p", Text: "Hello ", Edit: &documentSemanticEdit{From: 2, To: 2, Insert: "llo"}, Rich: &documentRichProof{Before: raw, After: next}}
	in, err := documentProposalInput("prop", "f", "run", "p", 5, semantic, map[string]string{"p": hash}, 0, &generation)
	if err != nil || in.Schema != 5 {
		t.Fatalf("schema-5 proposal: %+v %v", in, err)
	}
	for _, schema := range []int{3, 4, 6} {
		if _, err := documentProposalInput("prop", "f", "run", "p", schema, semantic, map[string]string{"p": hash}, 0, &generation); err == nil {
			t.Fatalf("schema-5 content proposed at schema %d", schema)
		}
	}
}
