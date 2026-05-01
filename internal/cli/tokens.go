package cli

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// EstimateTokens returns an approximate token count for `s`.
//
// Heuristic: ~4 characters per token for English-leaning text, with a
// minimum of 1 token for non-empty input. This matches the rule of
// thumb used by Anthropic / OpenAI tokenizers within ~10-20% on prose;
// code and structured payloads can drift further. We use rune count
// rather than byte count so multi-byte UTF-8 doesn't double-count Czech
// or other accented text the way naive `len(s)` would.
//
// Why a heuristic instead of a real tokenizer:
//   - The CLI ships as a small static binary; pulling in tiktoken or
//     similar adds ~1 MB and a binary dependency.
//   - Estimates here are used for budget previews and dry-run diagnostics,
//     not billing — being off by 15% is acceptable; ten minutes of import
//     wrangling for accuracy that won't change a user's decision is not.
//   - When users need exact counts, the paymaster journal entries record
//     real token usage post-call.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := utf8.RuneCountInString(s)
	t := runes / 4
	if t < 1 {
		t = 1
	}
	return t
}

// FormatEstimate returns a human-readable multi-line description of the
// prompt's size, token count, and rough input-side cost ranges across a
// few canonical models. Used by --estimate and the prompt-summary card.
//
// The model rates here are Anthropic's published list-prices for INPUT
// tokens (per 1M) as of late April 2026:
//
//	Sonnet 4.6: $3
//	Opus 4.7:   $15
//	Haiku 4.5:  $1
//
// These are intentionally hard-coded — pulling them dynamically from the
// paymaster `model_rates` table would couple the CLI to a server round-trip
// just for a dry-run UX, which is the wrong tradeoff. If they get stale,
// they're a 3-line patch.
func FormatEstimate(prompt string) string {
	chars := utf8.RuneCountInString(prompt)
	tokens := EstimateTokens(prompt)

	var sb strings.Builder
	fmt.Fprintf(&sb, "prompt size: %s chars (≈%s tokens)\n",
		formatThousands(chars), formatThousands(tokens))
	fmt.Fprintf(&sb, "lines: %d\n", strings.Count(prompt, "\n")+1)

	per1M := func(rate float64) string {
		cost := float64(tokens) / 1_000_000 * rate
		return fmt.Sprintf("$%.4f", cost)
	}
	fmt.Fprintf(&sb, "estimated input cost (heuristic):\n")
	fmt.Fprintf(&sb, "  Sonnet 4.6: %s    @ $3/M\n", per1M(3))
	fmt.Fprintf(&sb, "  Opus 4.7:   %s    @ $15/M\n", per1M(15))
	fmt.Fprintf(&sb, "  Haiku 4.5:  %s    @ $1/M\n", per1M(1))
	fmt.Fprintf(&sb, "(output cost depends on response length)\n")
	return sb.String()
}

// formatThousands returns n with comma separators, e.g. 1234567 -> "1,234,567".
// Avoids a strconv import — n is small enough that hand-formatting is trivial.
func formatThousands(n int) string {
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	if n == 0 {
		return "0"
	}
	var buf [32]byte
	i := len(buf)
	count := 0
	for n > 0 {
		if count > 0 && count%3 == 0 {
			i--
			buf[i] = ','
		}
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
		count++
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
