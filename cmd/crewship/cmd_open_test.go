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
		{"dashboard alias goes to inbox", "http://localhost:8080", []string{"dashboard"}, "http://localhost:8080/inbox", false},
		{"home alias goes to inbox", "http://localhost:8080", []string{"home"}, "http://localhost:8080/inbox", false},
		{"inbox", "http://localhost:8080", []string{"inbox"}, "http://localhost:8080/inbox", false},
		{"activity", "http://localhost:8080", []string{"activity"}, "http://localhost:8080/activity", false},
		{"agents list", "http://localhost:8080", []string{"agents"}, "http://localhost:8080/agents", false},
		{"agent detail", "http://localhost:8080", []string{"agent", "viktor"}, "http://localhost:8080/agents/viktor", false},
		{"crew detail", "http://localhost:8080", []string{"crew", "backend-team"}, "http://localhost:8080/crews/backend-team", false},
		{"chat by agent slug", "http://localhost:8080", []string{"chat", "viktor"}, "http://localhost:8080/chat/viktor", false},
		{"mission timeline", "http://localhost:8080", []string{"mission", "MIS-42"}, "http://localhost:8080/missions/MIS-42/timeline", false},
		{"journal", "http://localhost:8080", []string{"journal"}, "http://localhost:8080/journal", false},
		{"approvals", "http://localhost:8080", []string{"approvals"}, "http://localhost:8080/approvals", false},
		{"integrations", "http://localhost:8080", []string{"integrations"}, "http://localhost:8080/integrations", false},
		{"routines", "http://localhost:8080", []string{"routines"}, "http://localhost:8080/routines", false},
		{"issues list", "http://localhost:8080", []string{"issues"}, "http://localhost:8080/issues", false},
		{"issue detail", "http://localhost:8080", []string{"issues", "ENG-7"}, "http://localhost:8080/issues/ENG-7", false},
		{"runs", "http://localhost:8080", []string{"runs"}, "http://localhost:8080/runs", false},
		{"settings", "http://localhost:8080", []string{"settings"}, "http://localhost:8080/settings", false},
		{"admin", "http://localhost:8080", []string{"admin"}, "http://localhost:8080/admin", false},
		{"audit", "http://localhost:8080", []string{"audit"}, "http://localhost:8080/audit", false},
		{"credentials", "http://localhost:8080", []string{"credentials"}, "http://localhost:8080/credentials", false},
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
