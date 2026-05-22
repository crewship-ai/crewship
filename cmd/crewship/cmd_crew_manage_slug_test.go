package main

import "testing"

// TestDeriveSlugFromName pins issue #533 — `crewship crew create --name X`
// without --slug used to send slug="" and trip the server's 400 gate.
// Helper now produces a kebab-case slug matching helpers.go validSlugRe
// (`^[a-z0-9][a-z0-9_-]*$`) for typical inputs.
func TestDeriveSlugFromName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"Engineering", "engineering"},
		{"Customer Support", "customer-support"},
		{"  Engineering  ", "engineering"},
		{"Engineering 🛠", "engineering"},
		{"engineering_v2", "engineering_v2"},
		{"Engineering/QA", "engineering-qa"},
		{"E", "e"},
		{"", ""},
		{"   ", ""},
		{"123-team", "123-team"},
		{"---hello---", "hello"},
		{"Café Crew", "caf-crew"}, // accent stripped, "Caf" + "-" + "Crew"
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			if got := deriveSlugFromName(c.in); got != c.want {
				t.Errorf("deriveSlugFromName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
