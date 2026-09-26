package main

// Acceptance for the `crewship chat list` server-side filters the chat
// workspace preview added beyond --kind: --search/-q, --chat, --routine and
// --with-source. Same hazard and same harness as
// acceptance_chat_list_kind_test.go: a filter is worth exactly nothing unless
// it reaches the server before LIMIT, so the stub asserts on the raw query
// string the built binary actually sent.
//
// --with-source has a second half that a query assertion cannot cover: the
// source objects the server then attaches have to survive the decode and
// reach BOTH outputs — the table's SOURCE column and the `source` key in
// --format json. A decode struct missing the field would pass every query
// test and still hide provenance from every consumer.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runChatListCLIFormat is runChatListCLI with the config's format settable —
// the decode assertion needs --format json without depending on flag order.
func runChatListCLIFormat(t *testing.T, serverURL, format string, args ...string) (string, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: " + format + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// chatListSourceFixture carries the two source shapes the server mints: a
// routine row (kind routine, run/step ids attached) and an issue row
// (kind issue, identifier in slug). A direct chat has no source at all —
// omitted by the server, and the row below proves the decoder tolerates
// its absence.
const chatListSourceFixture = `[
  {"id":"c_abc","title":"Deploy rollback","kind":"direct","origin":"UI","status":"ACTIVE",
   "message_count":14,"started_at":"2026-08-30T09:00:00Z","last_activity_at":"2026-08-30T09:00:00Z","unread_count":2},
  {"id":"run_1","title":"Daily digest · summarize","kind":"routine","origin":"ROUTINE","status":"ACTIVE",
   "message_count":2,"started_at":"2026-08-31T07:20:00Z","last_activity_at":"2026-08-31T07:20:00Z","unread_count":0,
   "source":{"kind":"routine","id":"pipe_1","name":"Daily digest","slug":"daily-digest","run_id":"prun_9","step_id":"step_2"}},
  {"id":"m_1","title":"Fix login flake","kind":"issue","origin":"UI","status":"ACTIVE",
   "message_count":6,"started_at":"2026-09-01T09:00:00Z","last_activity_at":"2026-09-01T09:00:00Z","unread_count":0,
   "source":{"kind":"issue","id":"m_1","name":"Fix login flake","slug":"CS-12"}}
]`

func TestAcceptance_ChatList_SendsSearchToTheServer(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--search", "rollback")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); !strings.Contains(q, "q=rollback") {
		t.Errorf("query = %q, want it to carry q=rollback", q)
	}
}

func TestAcceptance_ChatList_SearchHasShorthand(t *testing.T) {
	// --search is the spelled-out name, -q the shorthand a person types
	// twice a minute. Both must reach the server as q=.
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "-q", "rollback")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); !strings.Contains(q, "q=rollback") {
		t.Errorf("query = %q, want the -q shorthand to carry q=rollback", q)
	}
}

func TestAcceptance_ChatList_SearchIsEscaped(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--search", "deploy rollback")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	// Encoded, not raw: the escaping is what proves the phrase went through
	// url.Values rather than being concatenated into the path by hand.
	if q := stub.lastQuery(t); !strings.Contains(q, "q=deploy+rollback") {
		t.Errorf("query = %q, want the escaped phrase", q)
	}
}

func TestAcceptance_ChatList_SendsChatIDFilter(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--chat", "c_abc")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); !strings.Contains(q, "chat_id=c_abc") {
		t.Errorf("query = %q, want it to carry chat_id=c_abc", q)
	}
}

func TestAcceptance_ChatList_SendsRoutineFilter(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--routine", "pipe_1")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	if q := stub.lastQuery(t); !strings.Contains(q, "routine_id=pipe_1") {
		t.Errorf("query = %q, want it to carry routine_id=pipe_1", q)
	}
}

func TestAcceptance_ChatList_SendsSourceWhenAsked(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--with-source")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	// The literal "1", not a bool-flag-rendered "true": the server compares
	// the query value against exactly "1", so anything else asks for nothing
	// while looking like it worked.
	if q := stub.lastQuery(t); !strings.Contains(q, "source=1") {
		t.Errorf("query = %q, want it to carry source=1", q)
	}
}

func TestAcceptance_ChatList_FiltersCompose(t *testing.T) {
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey",
		"--kind", "routine", "--search", "digest", "--routine", "pipe_1",
		"--with-source", "--limit", "3")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	q := stub.lastQuery(t)
	for _, want := range []string{"kind=routine", "q=digest", "routine_id=pipe_1", "source=1", "limit=3"} {
		if !strings.Contains(q, want) {
			t.Errorf("query = %q, want it to carry %s", q, want)
		}
	}
}

func TestAcceptance_ChatList_NoFiltersWhenNotAsked(t *testing.T) {
	// Absent and empty mean the same thing to the server, but sending an
	// empty one anyway would put the command's DEFAULT behaviour at the
	// mercy of the parameter's parsing instead of the server's default.
	stub := &chatListStub{body: chatListFixture}
	srv := stub.start(t)

	out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey")
	if err != nil {
		t.Fatalf("chat list: %v\noutput: %s", err, out)
	}
	q := stub.lastQuery(t)
	for _, param := range []string{"q=", "chat_id=", "routine_id=", "source="} {
		if strings.Contains(q, param) {
			t.Errorf("query = %q, want no %s parameter at all", q, strings.TrimSuffix(param, "="))
		}
	}
}

func TestAcceptance_ChatList_DecodesSourceIntoTableAndJSON(t *testing.T) {
	stub := &chatListStub{body: chatListSourceFixture}
	srv := stub.start(t)

	t.Run("table gains a SOURCE column", func(t *testing.T) {
		out, err := runChatListCLI(t, srv.URL, "chat", "list", "casey", "--with-source")
		if err != nil {
			t.Fatalf("chat list: %v\noutput: %s", err, out)
		}
		if !strings.Contains(out, "SOURCE") {
			t.Errorf("no SOURCE column:\n%s", out)
		}
		// The name, not the raw id: a person reads "Daily digest", not
		// "pipe_1" — the id is what --format json is for.
		if !strings.Contains(out, "Daily digest") || !strings.Contains(out, "Fix login flake") {
			t.Errorf("source names not rendered:\n%s", out)
		}
	})

	t.Run("json keeps the source objects", func(t *testing.T) {
		out, err := runChatListCLIFormat(t, srv.URL, "json", "chat", "list", "casey", "--with-source")
		if err != nil {
			t.Fatalf("chat list: %v\noutput: %s", err, out)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("output is not a JSON array: %v\n%s", err, out)
		}
		sources := map[string]map[string]any{}
		sourceless := 0
		for _, r := range rows {
			s, _ := r["source"].(map[string]any)
			if s == nil {
				sourceless++
				continue
			}
			kind, _ := s["kind"].(string)
			sources[kind] = s
		}
		routine := sources["routine"]
		if routine == nil {
			t.Fatalf("no row carried a routine source object:\n%s", out)
		}
		// run_id and step_id are the fields that make a routine row
		// actionable (deep-link the run, the step); slug is what names an
		// issue. All three are omitempty on the wire, so asserting the
		// VALUES proves the decode kept the fields, not just the object.
		if routine["run_id"] != "prun_9" || routine["step_id"] != "step_2" {
			t.Errorf("routine source lost run/step ids: %v", routine)
		}
		if routine["slug"] != "daily-digest" {
			t.Errorf("routine source lost slug: %v", routine)
		}
		if issue := sources["issue"]; issue == nil || issue["slug"] != "CS-12" {
			t.Errorf("issue source missing or lost its identifier slug: %v", sources["issue"])
		}
		if sourceless == 0 {
			t.Errorf("a sourceless row did not survive the decode:\n%s", out)
		}
	})
}
