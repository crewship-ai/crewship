package chatbridge

import "testing"

func TestShouldAgentRespond(t *testing.T) {
	cases := []struct {
		name       string
		visibility string
		content    string
		slug       string
		want       bool
	}{
		{"private always responds", "private", "anything", "riley", true},
		{"legacy/unset visibility responds", "", "anything", "riley", true},
		{"group without mention stays silent", "group", "hey team what do you think", "riley", false},
		{"group with @mention responds", "group", "@riley can you check this", "riley", true},
		{"group mention mid-sentence", "group", "thanks, now @riley please run it", "riley", true},
		{"group mention case-insensitive", "group", "@Riley hello", "riley", true},
		{"group email is not a mention", "group", "send to email@riley.com", "riley", false},
		{"group partial slug is not a mention", "group", "@rileybot do it", "riley", false},
		{"group GROUP case-insensitive visibility", "GROUP", "no mention here", "riley", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldAgentRespond(c.visibility, c.content, c.slug); got != c.want {
				t.Errorf("ShouldAgentRespond(%q,%q,%q) = %v, want %v", c.visibility, c.content, c.slug, got, c.want)
			}
		})
	}
}
