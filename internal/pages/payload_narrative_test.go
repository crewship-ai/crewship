package pages

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/schemas"
)

// narrative.v1 is the panel an AI agent writes, so §8's rules are the test
// plan and not the preamble. §14 names three of them as executable tests:
// *"a narrative payload containing an image block, an external URL, or an
// undeclared action index is rejected at the API boundary."* All three are
// below, and each is rejected because the schema HAS NO FIELD for it — the
// rejection comes from `additionalProperties: false`, not from a sanitiser,
// which is the whole point of §2.4's constrained-schema principle.

func TestValidateNarrative_Accepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want func(*testing.T, *NarrativePayload)
	}{
		{
			name: "paragraphs and a verdict",
			raw: `{"verdict":"Three invoices are overdue.",
			       "blocks":[{"kind":"paragraph","text":"The ledger closed at 14:00."},
			                 {"kind":"paragraph","text":"Two suppliers have not confirmed."}]}`,
			want: func(t *testing.T, p *NarrativePayload) {
				if !p.HasVerdict() {
					t.Error("verdict was dropped")
				}
				if len(p.Blocks) != 2 {
					t.Errorf("blocks = %d, want 2", len(p.Blocks))
				}
			},
		},
		{
			name: "a list, which is consecutive list blocks",
			raw: `{"blocks":[{"kind":"paragraph","text":"Still open:"},
			                 {"kind":"list","text":"FA-2026-0041"},
			                 {"kind":"list","text":"FA-2026-0048"}]}`,
			want: func(t *testing.T, p *NarrativePayload) {
				if p.Blocks[1].Kind != BlockList || p.Blocks[2].Kind != BlockList {
					t.Errorf("list blocks did not survive: %+v", p.Blocks)
				}
			},
		},
		{
			// §8 rule 3's permitted half: an id and a kind, and the renderer
			// builds the URL from a route it already holds.
			name: "a block referencing an internal entity by id",
			raw: `{"blocks":[{"kind":"paragraph","text":"Opened for the finance crew.",
			                  "ref":{"kind":"issue","id":"1935"}}]}`,
			want: func(t *testing.T, p *NarrativePayload) {
				ref := p.Blocks[0].Ref
				if ref == nil || ref.Kind != EntityIssue || ref.ID != "1935" {
					t.Errorf("ref = %+v, want issue/1935", ref)
				}
			},
		},
		{
			// A measured "the agent ran and had nothing to say". Different
			// from never having pushed, which is the em dash (§9b.4).
			name: "no blocks at all",
			raw:  `{"blocks":[]}`,
			want: func(t *testing.T, p *NarrativePayload) {
				if p.Blocks == nil || len(p.Blocks) != 0 {
					t.Errorf("blocks = %v, want an empty non-nil slice", p.Blocks)
				}
				if p.HasVerdict() {
					t.Error("a payload with no verdict reports one")
				}
			},
		},
		{
			// §10b.7: a page renders the workspace's language. A check that
			// refuses Czech, Arabic or an emoji is not a security control.
			name: "non-ASCII prose",
			raw: `{"verdict":"Vše v pořádku.",
			       "blocks":[{"kind":"paragraph","text":"صافي الرصيد إيجابي ✅"}]}`,
			want: func(t *testing.T, p *NarrativePayload) {
				if !strings.Contains(p.Blocks[0].Text, "✅") {
					t.Error("the emoji was mangled")
				}
			},
		},
		{
			// Angle brackets are CHARACTERS. The defence against markup is
			// rule 10 — the renderer emits React text nodes — not a filter
			// that would also break "a < b" and every mention of a generic.
			name: "prose containing angle brackets",
			raw:  `{"blocks":[{"kind":"paragraph","text":"latency < 200 and queue depth > 0"}]}`,
			want: func(t *testing.T, p *NarrativePayload) {
				if !strings.Contains(p.Blocks[0].Text, "<") {
					t.Error("the payload was silently rewritten; the renderer is the control, not a filter")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ValidateNarrative([]byte(tc.raw))
			if err != nil {
				t.Fatalf("rejected a legal payload: %v", err)
			}
			if p.Schema() != SchemaNarrative {
				t.Errorf("Schema() = %q", p.Schema())
			}
			tc.want(t, p)
		})
	}
}

// §8 rule 2: *"No images in agent-authored content. None. Not sanitised —
// absent from the schema."* CamoLeak exfiltrated through a TRUSTED FIRST-PARTY
// image proxy and CSP did not help, so an allow-list is not the control. The
// control is that there is no field, and this is that assertion.
func TestValidateNarrative_NoImageFieldExists(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"an image block kind", `{"blocks":[{"kind":"image","text":"chart"}]}`},
		{"an image field on a block", `{"blocks":[{"kind":"paragraph","text":"see","image":"/logo.png"}]}`},
		{"an image_url field on a block", `{"blocks":[{"kind":"paragraph","text":"see","image_url":"cdn"}]}`},
		{"a thumbnail on a block", `{"blocks":[{"kind":"paragraph","text":"see","thumbnail":"x"}]}`},
		{"an icon on a block", `{"blocks":[{"kind":"paragraph","text":"see","icon":"alert"}]}`},
		{"a top-level images array", `{"blocks":[],"images":["a.png"]}`},
		{"an image on the ref", `{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"run","id":"r1","image":"a"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateNarrative([]byte(tc.raw)); err == nil {
				t.Fatal("an image reached the payload; §8 rule 2 says the schema has no field for one, " +
					"and CamoLeak is why an allow-list would not have been enough")
			}
		})
	}

	// Belt and braces: the published document never names one either, so an
	// IDE validating a producer script against it says the same thing.
	doc := strings.ToLower(string(narrativeSchemaBytes(t)))
	for _, banned := range []string{`"image"`, `"image_url"`, `"thumbnail"`, `"src"`, `"alt"`} {
		if strings.Contains(doc, banned+":") {
			t.Errorf("the published narrative schema declares %s as a property", banned)
		}
	}
}

// §8 rule 3: *"No free-form links… It may not carry a URL."* Slack AI's
// private-channel exfiltration was a rendered link. Two halves: no URL FIELD,
// and — because §14 asks for it in as many words — no URL smuggled into the
// prose either.
func TestValidateNarrative_NoURLReachesTheClient(t *testing.T) {
	t.Parallel()

	t.Run("no field can carry one", func(t *testing.T) {
		for _, raw := range []string{
			`{"blocks":[{"kind":"paragraph","text":"x","url":"https://evil.example/?q=secret"}]}`,
			`{"blocks":[{"kind":"paragraph","text":"x","href":"https://evil.example"}]}`,
			`{"blocks":[{"kind":"link","text":"click"}]}`,
			`{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"issue","id":"1","url":"https://evil.example"}}]}`,
			`{"blocks":[{"kind":"paragraph","text":"x","ref":{"url":"https://evil.example"}}]}`,
			`{"blocks":[],"links":[{"label":"docs","url":"https://evil.example"}]}`,
		} {
			if _, err := ValidateNarrative([]byte(raw)); err == nil {
				t.Errorf("a URL field was accepted: %s", raw)
			}
		}
	})

	t.Run("no URL smuggled into the prose", func(t *testing.T) {
		// The Slack AI shape: the private data is IN the address, and the leak
		// happens when a reader's client resolves it.
		hostile := []string{
			"https://evil.example/?q=api_key_abc",
			"http://10.0.0.1/collect",
			"//evil.example/collect",
			"JavaScript:fetch(1)",
			"data:text/html;base64,PHNjcmlwdD4=",
			"see WWW.evil.example for details",
			"mailto:exfil@evil.example",
		}
		for _, text := range hostile {
			raw, err := json.Marshal(map[string]any{
				"blocks": []any{map[string]any{"kind": "paragraph", "text": text}},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = ValidateNarrative(raw)
			if err == nil {
				t.Errorf("prose carrying %q was accepted; §14 requires a payload containing an "+
					"external URL to be rejected at the API boundary", text)
				continue
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Code != CodeInconsistentPayload {
				t.Errorf("%q: code = %v, want %s", text, err, CodeInconsistentPayload)
			}
		}
	})

	t.Run("the verdict is checked too", func(t *testing.T) {
		raw := `{"verdict":"details at https://evil.example/x","blocks":[]}`
		if _, err := ValidateNarrative([]byte(raw)); err == nil {
			t.Fatal("a URL in the verdict was accepted; it is the line a reader reads first")
		}
	})

	t.Run("ordinary prose that merely mentions a name is not a URL", func(t *testing.T) {
		// The check has to be narrow enough to stay on. A bare domain is inert
		// text: nothing resolves it, and refusing it would make the panel
		// unusable for the sentences agents actually write.
		for _, text := range []string{
			"the crewship.io docs cover this",
			"ratio 3/4 held all week",
			"see issue 1935 for the follow-up",
		} {
			raw, _ := json.Marshal(map[string]any{
				"blocks": []any{map[string]any{"kind": "paragraph", "text": text}},
			})
			if _, err := ValidateNarrative(raw); err != nil {
				t.Errorf("legal prose %q was refused: %v", text, err)
			}
		}
	})
}

// §8 rule 3's permitted half, and its boundary: an internal reference is an id
// and a kind. The id pattern admits no scheme and no slash, so it cannot be
// grown into a path or an absolute URL by a producer that read the schema.
func TestValidateNarrative_EntityRefCannotBecomeAnAddress(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"a scheme in the id", `{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"issue","id":"https://evil.example"}}]}`},
		{"a path in the id", `{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"issue","id":"../../admin"}}]}`},
		{"a slash in the id", `{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"run","id":"a/b"}}]}`},
		{"an unknown entity kind", `{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"webhook","id":"1"}}]}`},
		{"no id at all", `{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"issue"}}]}`},
		{"an empty id", `{"blocks":[{"kind":"paragraph","text":"x","ref":{"kind":"issue","id":""}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateNarrative([]byte(tc.raw)); err == nil {
				t.Fatal("accepted; a ref carries an id, and the renderer builds the URL")
			}
		})
	}

	// The shapes this repo actually issues all pass.
	for _, id := range []string{"1935", "run_8812", "nightly-close", "crew.finance"} {
		raw, _ := json.Marshal(map[string]any{
			"blocks": []any{map[string]any{
				"kind": "paragraph", "text": "x",
				"ref": map[string]any{"kind": "run", "id": id},
			}},
		})
		if _, err := ValidateNarrative(raw); err != nil {
			t.Errorf("a real id %q was refused: %v", id, err)
		}
	}
}

// §8 rule 1: *"The agent fills a schema; it never emits markup, HTML, CSS or
// code. narrative.v1 accepts typed blocks, not a markdown blob."*
func TestValidateNarrative_NoMarkupBlobField(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{"markdown":"# Report\n\nAll good"}`,
		`{"blocks":[],"html":"<b>hi</b>"}`,
		`{"blocks":[{"kind":"html","text":"<b>hi</b>"}]}`,
		`{"blocks":[{"kind":"code","text":"rm -rf /"}]}`,
		`{"blocks":[{"kind":"paragraph","text":"x","style":"color:red"}]}`,
		`{"blocks":[{"kind":"paragraph","text":"x","css":"a{}"}]}`,
		`{"text":"just a blob"}`,
	} {
		if _, err := ValidateNarrative([]byte(raw)); err == nil {
			t.Errorf("a markup or code blob was accepted: %s", raw)
		}
	}
}

// §12 stages narrative.v1 as text-only in v1, with actions in v1.1 behind the
// full §8 rule set. §8 rule 4 is why the field cannot simply be tolerated:
// *"Actions come from the page's declared allow-list only… The agent cannot
// author an action."* An `actions` array in the payload IS the agent authoring
// one, so it is refused rather than ignored — and §14's "undeclared action
// index" is rejected at the boundary because there is no index to declare yet.
func TestValidateNarrative_AgentCannotAuthorAnAction(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{"blocks":[],"actions":[{"label":"Restart","routine":"restart-api"}]}`,
		`{"blocks":[],"actions":[{"index":7}]}`,
		`{"blocks":[{"kind":"paragraph","text":"x","action":{"index":0}}]}`,
		`{"blocks":[{"kind":"action","text":"Restart"}]}`,
	} {
		if _, err := ValidateNarrative([]byte(raw)); err == nil {
			t.Errorf("an agent-authored action was accepted: %s", raw)
		}
	}
}

// The Trojan Source family. An agent that can flip the reading order of a
// sentence, or hide a word inside another word, can make the panel say the
// opposite of what the audit log stored.
func TestValidateNarrative_RejectsInvisibleAndBidiCharacters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		text string
	}{
		// Written as escapes on purpose: a literal one of these in this
		// file would make the test source itself the thing it warns about.
		{"right-to-left override", "the balance is \u202e000,1\u202c CZK"},
		{"left-to-right isolate", "ok \u2066reversed\u2069"},
		{"zero-width space", "app\u200broved"},
		{"word joiner", "de\u2060nied"},
		{"a byte order mark mid-string", "clean\ufeffup"},
		{"an escape character", "green\u001b[31m"},
		{"a NUL", "truncated\x00 and then some"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{
				"blocks": []any{map[string]any{"kind": "paragraph", "text": tc.text}},
			})
			if _, err := ValidateNarrative(raw); err == nil {
				t.Fatalf("accepted %q; what a reviewer approves and what a reader sees must be the same string", tc.text)
			}
		})
	}

	// Newlines and tabs are prose, and the marks that mixed-direction scripts
	// need to lay out are not overrides.
	for _, text := range []string{"line one\nline two", "a\tb", "\u0645\u0631\u062d\u0628\u0627\u200f world"} {
		raw, _ := json.Marshal(map[string]any{
			"blocks": []any{map[string]any{"kind": "paragraph", "text": text}},
		})
		if _, err := ValidateNarrative(raw); err != nil {
			t.Errorf("legal text %q was refused: %v", text, err)
		}
	}
}

// The rest of the shape: what a payload must have, and what it may not exceed.
func TestValidateNarrative_Rejects(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 2001)

	cases := []struct {
		name string
		raw  string
		code ErrorCode
	}{
		{"no blocks key at all", `{"verdict":"fine"}`, CodeSchemaViolation},
		{"a block with no kind", `{"blocks":[{"text":"x"}]}`, CodeSchemaViolation},
		{"a block with no text", `{"blocks":[{"kind":"paragraph"}]}`, CodeSchemaViolation},
		{"an empty block text", `{"blocks":[{"kind":"paragraph","text":""}]}`, CodeSchemaViolation},
		{"a null verdict", `{"verdict":null,"blocks":[]}`, CodeSchemaViolation},
		{"an empty verdict", `{"verdict":"","blocks":[]}`, CodeSchemaViolation},
		{"a block text past the cap", `{"blocks":[{"kind":"paragraph","text":"` + long + `"}]}`, CodeSchemaViolation},
		{"blocks as a string", `{"blocks":"all good"}`, CodeSchemaViolation},
		{"not JSON at all", `{"blocks":`, CodeInvalidJSON},
		// §4 rule 2: freshness is computed server-side from the timestamp the
		// server stored. A payload that could carry one would be a panel that
		// can claim to be current forever.
		{"a producer-supplied timestamp", `{"blocks":[],"produced_at":"2020-01-01T00:00:00Z"}`, CodeSchemaViolation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateNarrative([]byte(tc.raw))
			if err == nil {
				t.Fatal("accepted")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *ValidationError, got %T", err)
			}
			if ve.Code != tc.code {
				t.Errorf("code = %q, want %q (%v)", ve.Code, tc.code, err)
			}
			if ve.Schema != SchemaNarrative {
				t.Errorf("schema = %q, want %q", ve.Schema, SchemaNarrative)
			}
		})
	}
}

// Forty blocks is a panel; forty-one is a document, and the page has an issue
// for that.
func TestValidateNarrative_BlockCap(t *testing.T) {
	t.Parallel()

	build := func(n int) []byte {
		blocks := make([]any, 0, n)
		for i := 0; i < n; i++ {
			blocks = append(blocks, map[string]any{"kind": "list", "text": "item"})
		}
		raw, _ := json.Marshal(map[string]any{"blocks": blocks})
		return raw
	}
	if _, err := ValidateNarrative(build(maxNarrativeBlocks)); err != nil {
		t.Fatalf("%d blocks was refused: %v", maxNarrativeBlocks, err)
	}
	if _, err := ValidateNarrative(build(maxNarrativeBlocks + 1)); err == nil {
		t.Fatalf("%d blocks was accepted; the cap is %d", maxNarrativeBlocks+1, maxNarrativeBlocks)
	}
}

// ValidatePayload is the single entry point every write path uses — CLI,
// sidecar and inbound webhook — so narrative has to be reachable through it
// and not only through its own function.
func TestValidatePayload_DispatchesNarrative(t *testing.T) {
	t.Parallel()

	p, err := ValidatePayload(SchemaNarrative, []byte(`{"blocks":[{"kind":"paragraph","text":"ok"}]}`))
	if err != nil {
		t.Fatalf("ValidatePayload: %v", err)
	}
	if p.Schema() != SchemaNarrative {
		t.Errorf("Schema() = %q", p.Schema())
	}
	if _, ok := p.(*NarrativePayload); !ok {
		t.Errorf("got %T, want *NarrativePayload", p)
	}
}

func narrativeSchemaBytes(t *testing.T) []byte {
	t.Helper()
	return schemas.PanelNarrativeV1
}
