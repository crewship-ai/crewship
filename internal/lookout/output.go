package lookout

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// fenceRe matches a fenced code block, optionally tagged with a language
// (```json, ```, etc.). The content group is non-greedy so multiple fences
// in a single response don't get glued together.
var fenceRe = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n?(.*?)```")

// MaxRetryAttempts is the cap that callers should respect when running the
// parse-validate-correct loop. Three is a balance: one shot lets the LLM
// fix the obvious typo, two shots covers the case where the first
// correction introduced a new error, three shots is the practical ceiling
// before paying down latency for diminishing returns.
const MaxRetryAttempts = 3

// ParseStructured extracts JSON from raw, validates it against schema, and
// returns the parsed map. It tries (in order):
//  1. raw as-is, in case the model returned bare JSON;
//  2. the contents of every fenced code block, picking the first that parses;
//  3. the substring between the first '{' and the matching last '}' as a
//     last-ditch heuristic for chatty outputs.
//
// Returns an *ArgsInvalidError on schema violation so callers can drive
// the retry loop with the same error type used by the args layer.
func ParseStructured(raw string, schema Schema) (map[string]any, error) {
	candidates := jsonCandidates(raw)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("lookout: no JSON object found in output")
	}
	var parsed map[string]any
	var lastErr error
	for _, c := range candidates {
		var v any
		if err := json.Unmarshal([]byte(c), &v); err != nil {
			lastErr = err
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			lastErr = fmt.Errorf("lookout: top-level JSON value is not an object")
			continue
		}
		parsed = obj
		lastErr = nil
		break
	}
	if parsed == nil {
		if lastErr != nil {
			return nil, fmt.Errorf("lookout: parse output: %w", lastErr)
		}
		return nil, fmt.Errorf("lookout: no parseable JSON in output")
	}
	if err := ValidateArgs(schema, parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// jsonCandidates returns the substrings of raw that are most likely to be
// the JSON payload. Order matters: callers walk the slice and pick the
// first that parses.
func jsonCandidates(raw string) []string {
	out := []string{strings.TrimSpace(raw)}
	for _, m := range fenceRe.FindAllStringSubmatch(raw, -1) {
		if len(m) >= 2 {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	if start := strings.IndexByte(raw, '{'); start >= 0 {
		if end := strings.LastIndexByte(raw, '}'); end > start {
			out = append(out, strings.TrimSpace(raw[start:end+1]))
		}
	}
	// Drop empties and exact duplicates while preserving order.
	seen := make(map[string]struct{}, len(out))
	dedup := make([]string, 0, len(out))
	for _, c := range out {
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		dedup = append(dedup, c)
	}
	return dedup
}

// RetryWithCorrection turns a validation error into a follow-up prompt the
// caller can append to the LLM conversation. It deliberately keeps the
// message short and concrete so the model doesn't drift on retry. The raw
// argument is intentionally unused in the body — quoting the model's own
// faulty output back at it has been observed to anchor it on the bad
// version. Future revisions may include a diff but for now the policy is
// "describe the error, ask for clean JSON".
func RetryWithCorrection(raw string, validationErr error) string {
	hint := describeForRetry(validationErr)
	return fmt.Sprintf(
		"Your previous response did not match the required schema: %s. "+
			"Reply again with ONLY a single JSON object that satisfies the schema, "+
			"no markdown fences, no commentary.",
		hint,
	)
}
