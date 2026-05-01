package main

import (
	"strings"
	"testing"
)

func TestBuildOpenURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		args []string
		want string
		err  bool
	}{
		{"dashboard", "http://localhost:8080", []string{"dashboard"}, "http://localhost:8080/", false},
		{"agents list", "http://localhost:8080", []string{"agents"}, "http://localhost:8080/agents", false},
		{"agent detail", "http://localhost:8080", []string{"agent", "viktor"}, "http://localhost:8080/agents/viktor", false},
		{"crew detail", "http://localhost:8080", []string{"crew", "backend-team"}, "http://localhost:8080/crews/backend-team", false},
		{"chat", "http://localhost:8080", []string{"chat", "c_abc123"}, "http://localhost:8080/chat?id=c_abc123", false},
		{"mission", "http://localhost:8080", []string{"mission", "MIS-42"}, "http://localhost:8080/missions/MIS-42", false},
		{"journal", "http://localhost:8080", []string{"journal"}, "http://localhost:8080/journal", false},
		{"approvals", "http://localhost:8080", []string{"approvals"}, "http://localhost:8080/approvals", false},
		{"paymaster alias", "http://localhost:8080", []string{"cost"}, "http://localhost:8080/paymaster", false},
		{"crows-nest no id", "http://localhost:8080", []string{"crows-nest"}, "http://localhost:8080/crows-nest", false},
		{"crows-nest with id", "http://localhost:8080", []string{"crows-nest", "team1"}, "http://localhost:8080/crows-nest/team1", false},
		{"agent missing id", "http://localhost:8080", []string{"agent"}, "", true},
		{"chat missing id", "http://localhost:8080", []string{"chat"}, "", true},
		{"unknown resource", "http://localhost:8080", []string{"bogus"}, "", true},
		{"trailing slash trimmed", "http://localhost:8080/", []string{"agents"}, "http://localhost:8080/agents", false},
		{"escapes special chars", "http://localhost:8080", []string{"agent", "weird:slug"}, "http://localhost:8080/agents/weird:slug", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildOpenURL(c.base, c.args)
			if c.err {
				if err == nil {
					t.Errorf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuildOpenURL_CaseInsensitive(t *testing.T) {
	got, err := buildOpenURL("http://localhost:8080", []string{"JOURNAL"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/journal") {
		t.Errorf("got %q, want /journal suffix", got)
	}
}
