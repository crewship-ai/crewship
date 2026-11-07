package pipeline

import (
	"encoding/json"
	"testing"
)

func TestEventFilterMatches(t *testing.T) {
	cases := []struct {
		name, filter, payload string
		want                  bool
	}{
		{"empty matches anything", "", "not json", true},
		{"string eq", "status == done", `{"status":"done"}`, true},
		{"string ne", "status == done", `{"status":"failed"}`, false},
		{"nested", "a.b.c == 1", `{"a":{"b":{"c":1}}}`, true},
		{"array index", "items.1.id == x", `{"items":[{"id":"w"},{"id":"x"}]}`, true},
		{"bool", "ok == true", `{"ok":true}`, true},
		{"null", "v == null", `{"v":null}`, true},
		{"and both", "a == 1 && b == 2", `{"a":1,"b":2}`, true},
		{"and one fails", "a == 1 && b == 2", `{"a":1,"b":3}`, false},
		{"quoted with and", `a == "x && y"`, `{"a":"x && y"}`, true},
		{"quoted empty", `a == ""`, `{"a":""}`, true},
		{"missing path", "a == 1", `{"b":1}`, false},
		{"non-scalar", "a == 1", `{"a":{"b":1}}`, false},
		{"non-json payload", "a == 1", `hello`, false},
		{"malformed never matches", "a", `{"a":1}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EventFilterMatches(c.filter, c.payload); got != c.want {
				t.Fatalf("EventFilterMatches(%q, %q) = %v, want %v", c.filter, c.payload, got, c.want)
			}
		})
	}
}

func TestValidateEventFilter(t *testing.T) {
	cases := []struct {
		filter string
		ok     bool
	}{
		{"", true},
		{"a == b", true},
		{"a.b == {{ inputs.x }}", true},
		{`{{ inputs.k }} == "v"`, true},
		{"a", false},
		{"a ==", false},
		{"== b", false},
		{"a..b == 1", false},
		{"a b == 1", false},
		{`a == "unterminated`, false},
		{"a == 1 &&", false},
		{"a == b == c", false},
	}
	for _, c := range cases {
		if err := ValidateEventFilter(c.filter); (err == nil) != c.ok {
			t.Errorf("ValidateEventFilter(%q) err=%v, want ok=%v", c.filter, err, c.ok)
		}
	}
}

func TestParseRejectsMalformedWaitEventFilter(t *testing.T) {
	def := func(f string) []byte {
		return []byte(`{"dsl_version":"1.0","name":"x","steps":[{"id":"w","type":"wait","wait":{"kind":"event","event_type":"e","event_filter":` + jsonQuote(f) + `}}]}`)
	}
	validate := func(f string) error {
		d, err := Parse(def(f))
		if err != nil {
			return err
		}
		return Validate(d, nil, nil)
	}
	if err := validate("status == done"); err != nil {
		t.Fatalf("valid filter rejected: %v", err)
	}
	if err := validate("status"); err == nil {
		t.Fatal("malformed filter accepted at save time")
	}
}

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
