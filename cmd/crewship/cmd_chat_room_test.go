package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestChatRoomAPIContracts(t *testing.T) {
	guardCLIState(t)
	tests := []struct {
		name                                string
		args                                []string
		method, path, query, body, response string
		status                              int
	}{
		{"direct to fresh group", []string{"continue", "room1", "--title", " Team ", "--member", "u3", "--client-id", "continue-1"}, "POST", "/room1/continue", "", `{"kind":"group","title":"Team","member_ids":["u3"],"agent_id":"","client_id":"continue-1"}`, `{"id":"group1","kind":"group","is_direct":false,"last_sequence":0}`, 201},
		{"activity get", []string{"activity", "room1"}, "GET", "/room1/activity", "", "", `{"issues":false,"routines":false}`, 200},
		{"activity set", []string{"activity", "room1", "--issues=true", "--routines=false"}, "PUT", "/room1/activity", "", `{"issues":true,"routines":false}`, `{"issues":true,"routines":false}`, 200},
		{"list pagination", []string{"list", "--limit", "2", "--offset", "4"}, "GET", "", "limit=2&offset=4", "", `{"conversations":[],"next_offset":6}`, 200},
		{"create group", []string{"create", "--title", " Test ", "--member", "u1", "--member", "u2"}, "POST", "", "", `{"title":"Test","kind":"group","member_ids":["u1","u2"]}`, `{"id":"room1"}`, 201},
		{"create channel", []string{"create", "--title", "Team", "--kind", "channel"}, "POST", "", "", `{"title":"Team","kind":"channel","member_ids":[]}`, `{"id":"room1"}`, 201},
		{"direct", []string{"direct", "u2"}, "POST", "/direct", "", `{"user_id":"u2"}`, `{"id":"room1","is_direct":true}`, 200},
		{"get", []string{"get", "room1"}, "GET", "/room1", "", "", `{"id":"room1","unread_count":2}`, 200},
		{"latest", []string{"messages", "room1"}, "GET", "/room1/messages", "limit=100", "", `{"messages":[],"has_more":false}`, 200},
		{"catch up zero", []string{"messages", "room1", "--after-sequence", "0", "--limit", "2"}, "GET", "/room1/messages", "after_sequence=0&limit=2", "", `{"messages":[],"has_more":true}`, 200},
		{"older", []string{"messages", "room1", "--before-sequence", "40"}, "GET", "/room1/messages", "before_sequence=40&limit=100", "", `{"messages":[],"has_more":false}`, 200},
		{"two agent mentions", []string{"send", "room1", "-m", "Ahoj @Ava a @Theo", "--client-id", "retry-1", "--mention-agent", "a1", "--mention-agent", "a2"}, "POST", "/room1/messages", "", `{"content":"Ahoj @Ava a @Theo","client_id":"retry-1","mentioned_agent_ids":["a1","a2"]}`, `{"id":"m1","sequence":7}`, 201},
		{"plain mention no invocation", []string{"send", "room1", "-m", "@Ava", "--client-id", "retry-2"}, "POST", "/room1/messages", "", `{"content":"@Ava","client_id":"retry-2","mentioned_agent_ids":[]}`, `{"id":"m2","sequence":8}`, 200},
		{"read", []string{"read", "room1", "--sequence", "7"}, "POST", "/room1/read", "", `{"last_read_sequence":7}`, "", 204},
		{"mute", []string{"mute", "room1"}, "POST", "/room1/mute", "", `{"muted":true}`, "", 204},
		{"unmute", []string{"mute", "room1", "--muted=false"}, "POST", "/room1/mute", "", `{"muted":false}`, "", 204},
		{"members list", []string{"participants", "list", "room1"}, "GET", "/room1/participants", "", "", `{"participants":[]}`, 200},
		{"members add", []string{"participants", "add", "room1", "u2"}, "POST", "/room1/participants", "", `{"user_id":"u2"}`, "", 204},
		{"members remove", []string{"participants", "remove", "room1", "u2"}, "DELETE", "/room1/participants/u2", "", "", "", 204},
		{"agents list", []string{"agents", "list", "room1"}, "GET", "/room1/agents", "", "", `{"agents":[]}`, 200},
		{"agents add", []string{"agents", "add", "room1", "a2"}, "POST", "/room1/agents", "", `{"agent_id":"a2"}`, "", 204},
		{"agents remove", []string{"agents", "remove", "room1", "a2"}, "DELETE", "/room1/agents/a2", "", "", "", 204},
		{"jobs", []string{"jobs", "room1"}, "GET", "/room1/agent-jobs", "", "", `{"jobs":[{"state":"pending"}]}`, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tt.method || r.URL.Path != chatRoomBase+tt.path {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				q := r.URL.Query()
				if q.Get("workspace_id") != covWS {
					t.Errorf("workspace missing: %v", q)
				}
				q.Del("workspace_id")
				if q.Encode() != tt.query {
					t.Errorf("query %s want %s", q.Encode(), tt.query)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing caller auth")
				}
				raw, _ := io.ReadAll(r.Body)
				if tt.body != "" {
					var got, want any
					if err := json.Unmarshal(raw, &got); err != nil {
						t.Error(err)
					}
					_ = json.Unmarshal([]byte(tt.body), &want)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("body %s want %s", raw, tt.body)
					}
				} else if len(raw) > 0 {
					t.Errorf("unexpected body %s", raw)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.response)
			}))
			defer server.Close()
			setStubCLI(t, server.URL)
			flagFormat = "json"
			cmd := newChatRoomCmd()
			cmd.SetArgs(tt.args)
			out := captureStdoutCovCli2(t, func() {
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
			})
			if calls != 1 {
				t.Fatalf("expected exactly one request got %d", calls)
			}
			var got, want any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("non JSON stdout: %q: %v", out, err)
			}
			expected := tt.response
			if tt.status == 204 {
				expected = `{"status":"ok"}`
			}
			_ = json.Unmarshal([]byte(expected), &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("lost response data: %s", out)
			}
		})
	}
}

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

func TestChatRoomRetryKeepsIdentityAndMentions(t *testing.T) {
	guardCLIState(t)
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		requests = append(requests, string(raw))
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			w.WriteHeader(http.StatusCreated)
		}
		_, _ = io.WriteString(w, `{"id":"message-once","sequence":12,"client_id":"stable-key"}`)
	}))
	defer server.Close()
	setStubCLI(t, server.URL)
	flagFormat = "json"
	var outputs []string
	for range 2 {
		c := newChatRoomCmd()
		c.SetArgs([]string{"send", "room", "-m", "Ask both", "--client-id", "stable-key", "--mention-agent", "a1", "--mention-agent", "a2"})
		outputs = append(outputs, captureStdoutCovCli2(t, func() {
			if err := c.Execute(); err != nil {
				t.Fatal(err)
			}
		}))
	}
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatalf("retry changed request: %v", requests)
	}
	if outputs[0] != outputs[1] {
		t.Errorf("retry changed returned identity: %v", outputs)
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
