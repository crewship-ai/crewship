package pipeline

import (
	"encoding/json"
	"strings"
)

// LLM outputs that need to round-trip through json.Unmarshal show up
// in four places: schema gate, outcomes grader, jsonPath template
// resolver, and the transform step. Every one of them used to crash
// on the same handful of LLM quirks (markdown fences, prose preamble,
// trailing chatter). Centralising the prep step here keeps the
// behaviour consistent and lets a single test matrix cover them all.

// ExtractJSONCandidate prepares an LLM-emitted string for json.Decoder.
// LLMs ignore "no prose outside the JSON" prompts in three reliable
// ways; fixing the per-pipeline prompt is whack-a-mole, fixing the
// extractor handles every existing and future routine in one place.
//
// Steps, in order:
//  1. Strip a leading/trailing markdown code fence (```json … ``` or
//     ``` … ```). Truncated streams without a closing fence still get
//     the opener stripped so the next step can find a delimiter.
//  2. If the result still doesn't start with a JSON delimiter ('{' or
//     '['), scan forward to the first one and slice from there. That
//     swallows preamble prose like "Here is the JSON:". Trailing
//     prose is handled by json.Decoder downstream, which reads one
//     value and stops at the first complete delimiter pair.
//
// If neither step finds a candidate, the original string is returned
// so the Decoder can produce its own error message rather than us
// silently mangling unrelated input.
func ExtractJSONCandidate(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "```") {
		// Drop the opening fence line. Anything until the first
		// newline is the language tag we discard. Single-line fences
		// (rare) get the bare prefix stripped.
		if nl := strings.IndexByte(t, '\n'); nl >= 0 {
			t = t[nl+1:]
		} else {
			t = strings.TrimPrefix(t, "```")
		}
		t = strings.TrimRight(t, " \t\n")
		t = strings.TrimSuffix(t, "```")
		t = strings.TrimSpace(t)
	}
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return t
	}
	// Scan for the earliest JSON delimiter. Whichever comes first wins.
	obj := strings.IndexByte(t, '{')
	arr := strings.IndexByte(t, '[')
	switch {
	case obj < 0 && arr < 0:
		return s
	case obj < 0:
		return t[arr:]
	case arr < 0:
		return t[obj:]
	case obj < arr:
		return t[obj:]
	default:
		return t[arr:]
	}
}

// DecodeAgentJSON unmarshals an LLM-emitted string into v after
// running it through ExtractJSONCandidate. Uses json.Decoder so a
// trailing prose suffix (e.g. "Let me know if you need anything
// else.") doesn't cause the parse to fail.
func DecodeAgentJSON(raw string, v any) error {
	dec := json.NewDecoder(strings.NewReader(ExtractJSONCandidate(raw)))
	return dec.Decode(v)
}

// DecodeAgentJSONNumber is DecodeAgentJSON but decodes JSON numbers as
// json.Number rather than float64, preserving integer width and decimal
// precision. The transform runner uses this so `@json` canonicalisation is
// byte-stable for large or high-precision numbers — a plain float64 decode
// round-trips e.g. 12345678901234567890 to 1.2345678901234568e+19, defeating
// the whole point of a reproducible canonical form. The schema-validation and
// grader paths deliberately keep DecodeAgentJSON's float64 decode, since the
// jsonschema validator expects float64 for its numeric constraints.
func DecodeAgentJSONNumber(raw string, v any) error {
	dec := json.NewDecoder(strings.NewReader(ExtractJSONCandidate(raw)))
	dec.UseNumber()
	return dec.Decode(v)
}
