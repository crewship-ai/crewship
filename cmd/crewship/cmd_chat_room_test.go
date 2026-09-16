package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The request/response contract of every room subcommand is proved against
// the real router by acceptance_chat_room_test.go, which drives the built
// binary. What stays here is the CLI-side behaviour a stub is the right tool
// for: validation that must refuse BEFORE any request is made, server
// errors surfacing as a non-zero exit, and output-format handling.

func TestChatRoomRejectsInvalidInputBeforeRequest(t *testing.T) {
	guardCLIState(t)
	for _, args := range [][]string{
		{"continue", "room", "--title", "Team"}, {"continue", "room", "--member", "u3"},
		{"activity", "room", "--issues=true"}, {"activity", "room", "--routines=false"},
		{"list", "--limit", "101"}, {"list", "--offset", "-1"},
		{"create", "--title", " "}, {"create", "--title", strings.Repeat("a", 121)}, {"create", "--title", "X", "--kind", "direct"},
		{"send", "room", "-m", "hello"}, {"send", "room", "--client-id", "key"}, {"send", "room", "-m", strings.Repeat("x", 32769), "--client-id", "key"},
		{"messages", "room", "--after-sequence", "0", "--before-sequence", "4"}, {"messages", "room", "--before-sequence", "0"}, {"messages", "room", "--after-sequence", "-1"}, {"messages", "room", "--limit", "0"},
		{"read", "room"}, {"read", "room", "--sequence", "-1"}, {"direct"}, {"agents", "add", "room"},
	} {
		t.Run(strings.Join(args[:min(2, len(args))], " "), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
			defer s.Close()
			setStubCLI(t, s.URL)
			c := newChatRoomCmd()
			c.SilenceUsage = true
			c.SilenceErrors = true
			c.SetArgs(args)
			if err := c.Execute(); err == nil {
				t.Error("expected validation error")
			}
			if calls != 0 {
				t.Errorf("invalid command made %d requests", calls)
			}
		})
	}
}

func TestChatRoomSurfacesServerErrors(t *testing.T) {
	guardCLIState(t)
	for _, code := range []int{401, 403, 404, 409, 429, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, `{"error":"room request denied"}`)
			}))
			defer s.Close()
			setStubCLI(t, s.URL)
			c := newChatRoomCmd()
			c.SilenceUsage = true
			c.SilenceErrors = true
			c.SetArgs([]string{"send", "room", "-m", "hello", "--client-id", "retry-key"})
			if err := c.Execute(); err == nil {
				t.Fatal("server failure reported as success")
			}
		})
	}
}

func TestChatRoomOutputFormatsAndEscapedIDs(t *testing.T) {
	guardCLIState(t)
	for _, format := range []string{"json", "yaml", "ndjson", "quiet", "table"} {
		t.Run(format, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.EscapedPath(), "/room%3Fspecial%23id") {
					t.Errorf("unescaped ID: %s", r.URL.EscapedPath())
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"room123","title":"Český tým","last_sequence":7}`)
			}))
			defer server.Close()
			setStubCLI(t, server.URL)
			flagFormat = format
			c := newChatRoomCmd()
			c.SetArgs([]string{"get", "room?special#id"})
			out := captureStdoutCovCli2(t, func() {
				if err := c.Execute(); err != nil {
					t.Fatal(err)
				}
			})
			if !strings.Contains(out, "room123") {
				t.Errorf("missing ID: %q", out)
			}
			if format == "quiet" && strings.TrimSpace(out) != "room123" {
				t.Errorf("quiet output not ID: %q", out)
			}
			if format == "json" || format == "ndjson" {
				var body map[string]any
				if err := json.Unmarshal([]byte(out), &body); err != nil {
					t.Errorf("invalid machine output: %q", out)
				}
			}
		})
	}
}

func TestChatRoomRegisteredAlongsideLegacySessions(t *testing.T) {
	guardCLIState(t)
	for _, path := range [][]string{{"chat", "room", "send"}, {"chat", "rooms", "participants", "list"}, {"chat", "create"}, {"chat", "list"}, {"chat", "stream"}} {
		c, remaining, err := rootCmd.Find(path)
		if err != nil || len(remaining) != 0 || c.RunE == nil {
			t.Errorf("command %v unavailable: %v %v", path, remaining, err)
		}
	}
}
