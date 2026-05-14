package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsValidEmojiReaction(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// happy path — actual emoji
		{"thumbs_up", "👍", true},
		{"heart", "❤", true},
		{"red_heart_with_vs", "❤\uFE0F", true},
		{"face_with_tears_of_joy", "😂", true},
		{"compound_zwj", "👨\u200D💻", true}, // man + ZWJ + computer
		{"flag_us", "🇺🇸", true},            // regional indicators
		{"skin_tone", "👋🏽", true},          // wave + medium skin tone
		{"copyright", "©", true},
		{"keycap_digit", "1", true}, // bare digit accepted (would be 1️⃣ with VS+combiner)

		// XSS / HTML — must be rejected
		{"script_tag", "<scr>", false},
		{"img_onerror", "<img on", false},
		{"angle_bracket", "<", false},
		{"ampersand", "&", false},
		{"quote", "\"", false},
		{"plain_letter", "A", false},
		{"plain_word", "abcd", false},

		// length / empty
		{"empty", "", false},
		{"too_long_runes", "👍👍👍👍👍👍👍👍👍", false}, // 9 runes > 8
		{"max_length_runes", "👍👍👍👍👍👍👍👍", true}, // exactly 8

		// control characters
		{"newline", "\n", false},
		{"null_byte", "\x00", false},
		{"backspace", "\b", false},

		// BiDi / direction overrides — must NOT pass.
		// Use escapes so the invisible format chars survive editor copy-paste.
		{"bidi_lro", "\u202D", false}, // LEFT-TO-RIGHT OVERRIDE
		{"bidi_rlo", "\u202E", false}, // RIGHT-TO-LEFT OVERRIDE

		// composition rules — added per CodeRabbit "enforce sequence
		// not just rune membership" review note. Each of these would
		// have passed the original rune-membership check; the new
		// state-machine validator rejects them. Escape sequences keep
		// invisible format chars (ZWJ, VS-16, keycap combiner) visible
		// in source so the cases don't silently rot.
		{"lone_skin_tone", "\U0001F3FB", false},                               // skin-tone modifier with no base
		{"lone_zwj", "\u200D", false},                                         // ZWJ alone
		{"trailing_zwj", "\U0001F468\u200D", false},                           // 👨 + dangling ZWJ
		{"leading_zwj", "\u200D\U0001F468", false},                            // ZWJ before any base
		{"lone_vs16", "\uFE0F", false},                                        // VS-16 alone
		{"lone_keycap_combiner", "\u20E3", false},                             // keycap combiner alone
		{"odd_regional_count_one", "\U0001F1FA", false},                       // single regional half
		{"odd_regional_count_three", "\U0001F1FA\U0001F1F8\U0001F1E8", false}, // three regional halves
		{"flag_plus_emoji", "\U0001F1FA\U0001F1F8\U0001F44D", true},           // 🇺🇸👍 — complete flag pair + thumbs up
		{"odd_regional_then_base", "\U0001F1FA\U0001F44D", false},             // single regional half + base
		{"valid_compound_zwj", "\U0001F468\u200D\U0001F4BB", true},            // 👨‍💻
		{"valid_skin_tone_after_base", "\U0001F44B\U0001F3FD", true},          // 👋🏽
		{"valid_base_then_vs16", "❤\uFE0F", true},                             // ❤️
		{"double_zwj", "\U0001F468\u200D\u200D\U0001F4BB", false},             // 👨 + ZWJ ZWJ + 💻
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isValidEmojiReaction(tc.in), tc.in)
		})
	}
}
