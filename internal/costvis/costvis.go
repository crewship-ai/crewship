// Package costvis is the one place that knows which wire fields carry money
// (#3590). Cost figures are readable by workspace OWNER and ADMIN only; every
// other role gets the same response with these fields left out. The role
// decision lives with the caller (internal/api canReadCosts, the WebSocket
// delivery gate); this package only removes the amounts.
//
// Redaction omits a field rather than writing 0 or null, so a client cannot
// mistake "not yours to see" for "free".
package costvis

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// Keys are the JSON object keys whose value is a money amount (USD) or a
// ratio of spend against a money budget. Matching is exact and applies at
// any depth, including inside journal payloads and run metadata.
//
// Deliberately absent: token counts (not money), a routine definition's
// authored per-run cap max_cost_usd (configuration the author set, not spend),
// and model price catalogs (public list prices, not this workspace's spend).
var Keys = map[string]struct{}{
	"cost_usd":             {},
	"total_cost_usd":       {},
	"costUSD":              {}, // agent CLI model_usage rows carry a per-model cost under this key
	"estimated_cost":       {},
	"estimated_cost_usd":   {},
	"total_estimated_cost": {},
	"estimate_usd":         {},
	"cost_usd_this_month":  {},
	"cost_30d_usd":         {},
	"total_cost_24h":       {},
	"spent_usd":            {},
	"limit_usd":            {},
	"remaining_usd":        {},
	"monthly_budget_usd":   {},
	"total_budget_usd":     {},
	"total_spent_usd":      {},
	"pct_used":             {},
	"util_pct":             {},
}

// Redact removes every cost key from v in place and returns it. v is the
// generic form encoding/json produces (map[string]any, []any, scalars).
// String values under "summary" are scrubbed of formatted amounts too, since
// journal summaries are rendered from the same figures.
func Redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if _, money := Keys[k]; money {
				delete(t, k)
				continue
			}
			if k == "summary" {
				if s, ok := child.(string); ok {
					t[k] = ScrubSummary(s)
					continue
				}
			}
			t[k] = Redact(child)
		}
	case []any:
		for i := range t {
			t[i] = Redact(t[i])
		}
	}
	return v
}

// RedactValue round-trips any Go value through JSON and redacts it. Use it
// for typed response structs; the result marshals to the same shape minus the
// cost fields.
func RedactValue(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep ids and large integers exact
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	return Redact(generic), nil
}

// MayCarry is a cheap pre-check over encoded JSON: false means no cost key can
// be present, so the caller can skip the decode. It may report true for a
// frame that has none (a key name inside a string), never false for one that
// has.
func MayCarry(raw []byte) bool {
	if bytes.Contains(raw, []byte(`$`)) {
		return true
	}
	for k := range Keys {
		if bytes.Contains(raw, []byte(`"`+k+`"`)) {
			return true
		}
	}
	return false
}

// RedactJSON redacts an encoded JSON document. On a decode failure it returns
// the input unchanged together with the error, and the caller decides
// (the WebSocket gate drops the frame rather than leak it).
func RedactJSON(raw []byte) ([]byte, error) {
	if !MayCarry(raw) {
		return raw, nil
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return raw, err
	}
	out, err := json.Marshal(Redact(generic))
	if err != nil {
		return raw, err
	}
	return out, nil
}

var (
	// "budget warning: crew/monthly at 85.0% ($8.50 / $10.00)"
	budgetPair = regexp.MustCompile(`\s*\(\$\d[\d,]*(?:\.\d+)? / \$\d[\d,]*(?:\.\d+)?\)`)
	// "openai/gpt: 10 in / 20 out tokens, $0.0040"
	trailingAmount = regexp.MustCompile(`,\s*\$\d[\d,]*(?:\.\d+)?$`)
	// "$0.0040 spent on openai/gpt"
	leadingSpent = regexp.MustCompile(`^\$\d[\d,]*(?:\.\d+)? spent on `)
	// any other machine-formatted amount (two or more decimals): "$1.50"
	anyAmount = regexp.MustCompile(`\$\d[\d,]*\.\d{2,}`)
)

// ScrubSummary removes formatted dollar amounts from a journal summary while
// keeping the sentence readable. Plain "$5" in human prose is left alone; the
// machine formats always carry two or more decimals.
func ScrubSummary(s string) string {
	if !bytes.Contains([]byte(s), []byte("$")) {
		return s
	}
	s = budgetPair.ReplaceAllString(s, "")
	s = trailingAmount.ReplaceAllString(s, "")
	s = leadingSpent.ReplaceAllString(s, "Spend recorded on ")
	s = anyAmount.ReplaceAllString(s, "(amount hidden)")
	return s
}
