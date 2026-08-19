package api

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

// The URL of an embed panel is resolved on the READ path from the operator's
// allow-list, and there is no path by which a producer's bytes become one.
//
// This is the load-bearing test of the whole embed design. An iframe's src is
// fetched by the reader's browser when the page opens, so a producer-settable
// URL is an outbound channel with a human trigger — encode the panel's numbers
// into a path, push, wait. Every case below is a way someone might try to
// smuggle one in.
func TestEmbedWire_ResolvesFromThePolicyAndNeverFromThePayload(t *testing.T) {
	policy, err := pages.ParseEmbedPolicy("grafana=https://grafana.example.com/d/abc", "https://crewship.example.com")
	if err != nil {
		t.Fatalf("ParseEmbedPolicy: %v", err)
	}
	defer pages.SetEmbedPolicy(policy)()

	h := &PageHandler{}

	cases := []struct {
		name    string
		schema  string
		payload string
		want    string
	}{
		{"a vetted name resolves", "embed.v1", `{"source":"grafana"}`, "https://grafana.example.com/d/abc"},
		{"an unknown name resolves to nothing", "embed.v1", `{"source":"nope"}`, ""},
		{"a url in the payload is not a url", "embed.v1", `{"source":"https://evil.example.com/?leak=42"}`, ""},
		{"a url key beside the source is ignored", "embed.v1", `{"source":"grafana","url":"https://evil.example.com"}`, "https://grafana.example.com/d/abc"},
		{"another schema never carries one", "metric.v1", `{"source":"grafana","value":1}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := h.embedWire(&panelRecord{Schema: tc.schema, Payload: tc.payload})
			if tc.want == "" {
				if got != nil {
					t.Fatalf("got %+v, want no embed", got)
				}
				return
			}
			if got == nil || got.URL != tc.want {
				t.Fatalf("got %+v, want url %q", got, tc.want)
			}
		})
	}
}

// With no allow-list — every instance that has not opted in — nothing resolves.
func TestEmbedWire_IsNothingWhenNoSourceIsConfigured(t *testing.T) {
	defer pages.SetEmbedPolicy(pages.EmbedPolicy{})()

	h := &PageHandler{}
	if got := h.embedWire(&panelRecord{Schema: "embed.v1", Payload: `{"source":"grafana"}`}); got != nil {
		t.Fatalf("got %+v, want no embed on an instance with no allow-list", got)
	}
}

// The wire carries the URL and nothing else. A sandbox attribute, an `allow`
// list or a height on the wire is a value something upstream could be
// persuaded to change; the client takes all three from constants.
func TestEmbedWire_CarriesTheURLAndNothingElse(t *testing.T) {
	raw, err := json.Marshal(pageEmbedWire{URL: "https://grafana.example.com/d/abc"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(fields) != 1 {
		t.Fatalf("the embed wire grew a field: %s", raw)
	}
	if _, ok := fields["url"]; !ok {
		t.Fatalf("no url on the wire: %s", raw)
	}
}
