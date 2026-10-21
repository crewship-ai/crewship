package costvis

import (
	"strings"
	"testing"
)

func TestRedactJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"top-level and nested keys go, others stay",
			`{"id":"r1","cost_usd":42,"rows":[{"spent_usd":1,"limit_usd":2,"state":"ok"}],"metadata":{"total_cost_usd":3,"model_usage":{"m":{"costUSD":3,"inputTokens":5}}}}`,
			`{"id":"r1","metadata":{"model_usage":{"m":{"inputTokens":5}}},"rows":[{"state":"ok"}]}`},
		{"budget keeps booleans",
			`{"has_budget":true,"over_budget":true,"monthly_budget_usd":10,"spent_usd":12,"pct_used":120}`,
			`{"has_budget":true,"over_budget":true}`},
		{"journal summary amounts scrubbed",
			`{"entry_type":"cost.incurred","summary":"$0.0040 spent on anthropic/claude","payload":{"cost_usd":0.004,"model":"claude"}}`,
			`{"entry_type":"cost.incurred","payload":{"model":"claude"},"summary":"Spend recorded on anthropic/claude"}`},
		{"authored per-run cap is configuration, not spend",
			`{"max_cost_usd":2}`,
			`{"max_cost_usd":2}`},
		{"frame without cost passes byte-identical",
			`{"type":"ping"}`,
			`{"type":"ping"}`},
		{"large integers stay exact",
			`{"seq":9007199254740993,"cost_usd":1}`,
			`{"seq":9007199254740993}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RedactJSON([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestScrubSummary(t *testing.T) {
	cases := []struct{ in, want string }{
		{"$0.0040 spent on openai/gpt", "Spend recorded on openai/gpt"},
		{"openai/gpt: 10 in / 20 out tokens, $0.0040", "openai/gpt: 10 in / 20 out tokens"},
		{"budget warning: crew/month at 85.0% ($8.50 / $10.00)", "budget warning: crew/month at 85.0%"},
		{"cost cap exceeded: $1.2500 > $1.0000 after step \"x\"", "cost cap exceeded: (amount hidden) > (amount hidden) after step \"x\""},
		{"pay $5 for coffee", "pay $5 for coffee"},
		{"no money here", "no money here"},
	}
	for _, c := range cases {
		if got := ScrubSummary(c.in); got != c.want {
			t.Errorf("ScrubSummary(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactJSON_InvalidIsAnError(t *testing.T) {
	in := `{"cost_usd":1`
	out, err := RedactJSON([]byte(in))
	if err == nil {
		t.Fatal("want error for truncated JSON")
	}
	if !strings.Contains(string(out), "cost_usd") {
		t.Fatal("on error the input is returned for the caller to drop, not a partial document")
	}
}
