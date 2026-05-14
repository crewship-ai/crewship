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
		{"red_heart_with_vs", "❤️", true},
		{"face_with_tears_of_joy", "😂", true},
		{"compound_zwj", "👨‍💻", true}, // man + ZWJ + computer
		{"flag_us", "🇺🇸", true},       // regional indicators
		{"skin_tone", "👋🏽", true},     // wave + medium skin tone
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

		// BiDi / direction overrides — must NOT pass
		{"bidi_lro", "‭", false}, // LEFT-TO-RIGHT OVERRIDE
		{"bidi_rlo", "‮", false}, // RIGHT-TO-LEFT OVERRIDE
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isValidEmojiReaction(tc.in), tc.in)
		})
	}
}
