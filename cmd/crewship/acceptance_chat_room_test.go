package main

// acceptance_chat_room_test.go — `crewship chat room …` against a real
// server (#2579).
//
// The 18 /api/v1/conversations routes (#2458) were covered on the CLI side
// only by an in-process stub that echoed whatever body the command sent.
// This file drives the BUILT BINARY through the real router over a migrated
// SQLite, as two workspace humans and one agent, and checks what only the
// real store can answer: that a DM is reused rather than duplicated, that a
// retried send returns the same message, that a mention leaves a pending
// job behind, that the creator-only and channel-only rules refuse with a
// non-zero exit, and that every subcommand's stdout is the server's
// envelope in the machine format.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptance_ChatRoom(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cchatroomworkspace0001"
	const agentID = "cchatroomagent0000000001"
	tokens := map[string]string{
		"room-owner": "crewship_cli_roomowner0000000000000000000",
		"room-bob":   "crewship_cli_roombob000000000000000000000",
		"room-carol": "crewship_cli_roomcarol00000000000000000000",
	}
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cchatroomworkspace0001','Rooms','rooms-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('cchatroomcrew00000000001','cchatroomworkspace0001','Ops','ops','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('room-owner','owner@example.invalid','Owner')`,
		`INSERT INTO users(id,email,full_name) VALUES('room-bob','bob@example.invalid','Bob')`,
		`INSERT INTO users(id,email,full_name) VALUES('room-carol','carol@example.invalid','Carol')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('room-m1','cchatroomworkspace0001','room-owner','OWNER')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('room-m2','cchatroomworkspace0001','room-bob','MEMBER')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('room-m3','cchatroomworkspace0001','room-carol','MEMBER')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('cchatroomagent0000000001','cchatroomcrew00000000001','cchatroomworkspace0001','Ava','ava')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for user, token := range tokens {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`,
			"tok-"+user, user, sha256HexToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()

	binary := buildCrewshipBinary(t)
	configFor := func(user, format string) string {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+tokens[user]+"\nformat: "+format+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	run := func(cfg string, args ...string) (string, error) {
		cmd := exec.Command(binary, append([]string{"chat", "room"}, args...)...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	owner, bob, carol := configFor("room-owner", "json"), configFor("room-bob", "json"), configFor("room-carol", "json")
	ownerTable := configFor("room-owner", "table")
	must := func(cfg string, args ...string) map[string]any {
		t.Helper()
		out, err := run(cfg, args...)
		if err != nil {
			t.Fatalf("chat room %v: %v\n%s", args, err, out)
		}
		body := map[string]any{}
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatalf("chat room %v did not print the server's JSON: %v\n%s", args, err, out)
		}
		return body
	}
	refuse := func(cfg string, want string, args ...string) {
		t.Helper()
		out, err := run(cfg, args...)
		if err == nil {
			t.Errorf("chat room %v: exit 0, want a refusal\n%s", args, out)
			return
		}
		if !strings.Contains(out, want) {
			t.Errorf("chat room %v: output lacks %q:\n%s", args, want, out)
		}
	}
	str := func(v any) string { s, _ := v.(string); return s }

	// ── create: a private group and a workspace channel ────────────────────
	group := must(owner, "create", "--title", " Planning ", "--member", "room-bob")
	groupID := str(group["id"])
	if groupID == "" || group["kind"] != "group" || group["title"] != "Planning" || group["is_direct"] != false {
		t.Fatalf("group = %v", group)
	}
	channel := must(owner, "create", "--title", "Ops channel", "--kind", "channel")
	channelID := str(channel["id"])
	if channelID == "" || channel["kind"] != "channel" {
		t.Fatalf("channel = %v", channel)
	}

	// ── direct: a fixed pair, reused on repeat ─────────────────────────────
	dm := must(owner, "direct", "room-bob")
	dmID := str(dm["id"])
	if dmID == "" || dm["is_direct"] != true {
		t.Fatalf("dm = %v", dm)
	}
	if again := must(owner, "direct", "room-bob"); str(again["id"]) != dmID {
		t.Errorf("a second direct created a new DM: %v", again)
	}
	if fromBob := must(bob, "direct", "room-owner"); str(fromBob["id"]) != dmID {
		t.Errorf("the same pair from the other side is a different DM: %v", fromBob)
	}
	refuse(owner, "invalid conversation request", "direct", "room-owner")

	// ── list: one page, offset cursor in the envelope ──────────────────────
	listed := must(owner, "list")
	rooms, _ := listed["conversations"].([]any)
	if len(rooms) != 3 {
		t.Errorf("owner sees %d rooms, want 3: %v", len(rooms), listed)
	}
	if listed["next_offset"] != nil {
		t.Errorf("a partial page carries a next_offset: %v", listed["next_offset"])
	}
	page := must(owner, "list", "--limit", "1", "--offset", "0")
	if rows, _ := page["conversations"].([]any); len(rows) != 1 || page["next_offset"] != float64(1) {
		t.Errorf("--limit 1 page = %v", page)
	}
	if carolRooms, _ := must(carol, "list")["conversations"].([]any); len(carolRooms) != 1 {
		// Carol is in no private room; the channel is workspace-visible.
		t.Errorf("carol sees %d rooms, want the channel only", len(carolRooms))
	}

	// ── get: metadata plus the caller's cursor ─────────────────────────────
	got := must(bob, "get", groupID)
	if got["title"] != "Planning" || got["unread_count"] != float64(0) || got["created_by"] != "room-owner" {
		t.Errorf("get as bob = %v", got)
	}
	refuse(carol, "conversation not found", "get", groupID)

	// ── agents: channel-only, creator-only ─────────────────────────────────
	if ok := must(owner, "agents", "add", channelID, agentID); ok["status"] != "ok" {
		t.Errorf("agents add = %v", ok)
	}
	agents, _ := must(bob, "agents", "list", channelID)["agents"].([]any)
	if len(agents) != 1 || str(agents[0].(map[string]any)["agent_id"]) != agentID || agents[0].(map[string]any)["slug"] != "ava" {
		t.Errorf("agents list = %v", agents)
	}
	refuse(owner, "invalid conversation request", "agents", "add", groupID, agentID)
	refuse(bob, "conversation not found", "agents", "add", channelID, agentID)

	// ── send: retry identity, mention → job ────────────────────────────────
	sent := must(owner, "send", channelID, "-m", "Ava, please look at the queue", "--client-id", "send-1", "--mention-agent", agentID)
	msgID := str(sent["id"])
	if msgID == "" || sent["sequence"] == nil {
		t.Fatalf("send = %v", sent)
	}
	if retry := must(owner, "send", channelID, "-m", "Ava, please look at the queue", "--client-id", "send-1", "--mention-agent", agentID); str(retry["id"]) != msgID {
		t.Errorf("a retried send minted a new message: %v", retry)
	}
	refuse(owner, "client_id already used", "send", channelID, "-m", "different text", "--client-id", "send-1")
	refuse(owner, "invalid conversation request", "send", groupID, "-m", "no agents in a group", "--client-id", "send-2", "--mention-agent", agentID)
	jobs, _ := must(owner, "jobs", channelID)["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v", jobs)
	}
	if job := jobs[0].(map[string]any); str(job["agent_id"]) != agentID || str(job["message_id"]) != msgID || job["state"] != "pending" {
		t.Errorf("job = %v", job)
	}

	// ── send without a mention: a plain message, no job ────────────────────
	plain := must(bob, "send", channelID, "-m", "Reading along", "--client-id", "plain-1")
	plainID := str(plain["id"])
	plainSeq, _ := plain["sequence"].(float64)
	if plainID == "" || plainID == msgID || plainSeq <= sent["sequence"].(float64) {
		t.Fatalf("mention-less send = %v", plain)
	}
	if mentions, ok := plain["mentioned_agent_ids"].([]any); !ok || len(mentions) != 0 {
		t.Errorf("mention-less send carries mentions: %v", plain["mentioned_agent_ids"])
	}
	if jobsAfter, _ := must(owner, "jobs", channelID)["jobs"].([]any); len(jobsAfter) != 1 {
		t.Errorf("a mention-less send queued a job: %v", jobsAfter)
	}

	// ── messages: latest page, older history, catch-up cursor ─────────────
	messages, _ := must(bob, "messages", channelID)["messages"].([]any)
	var lastSeq float64
	var sawSent bool
	for _, m := range messages {
		row := m.(map[string]any)
		if str(row["id"]) == msgID {
			sawSent = true
		}
		if seq, _ := row["sequence"].(float64); seq > lastSeq {
			lastSeq = seq
		}
	}
	if !sawSent || lastSeq < 1 {
		t.Errorf("messages = %v", messages)
	}
	if lastSeq != plainSeq {
		t.Errorf("newest sequence = %v, want the plain message's %v", lastSeq, plainSeq)
	}
	if later, _ := must(bob, "messages", channelID, "--after-sequence", seqArg(lastSeq))["messages"].([]any); len(later) != 0 {
		t.Errorf("--after-sequence <last> returned %d rows, want 0", len(later))
	}
	// Older history: everything before the plain message, the mention
	// message included, the plain one excluded — which is only true if the
	// flag reached the server as before_sequence.
	older := must(bob, "messages", channelID, "--before-sequence", seqArg(plainSeq), "--limit", "50")
	olderRows, _ := older["messages"].([]any)
	if len(olderRows) == 0 || older["has_more"] != false {
		t.Fatalf("--before-sequence = %v", older)
	}
	for _, m := range olderRows {
		row := m.(map[string]any)
		if seq, _ := row["sequence"].(float64); seq >= plainSeq {
			t.Errorf("--before-sequence returned sequence %v, not older than %v", seq, plainSeq)
		}
	}
	if last := olderRows[len(olderRows)-1].(map[string]any); str(last["id"]) != msgID {
		t.Errorf("older page does not end with the mention message: %v", last)
	}

	// ── read and mute: the caller's own state only ─────────────────────────
	if ok := must(bob, "read", channelID, "--sequence", seqArg(lastSeq)); ok["status"] != "ok" {
		t.Errorf("read = %v", ok)
	}
	if ok := must(bob, "mute", channelID); ok["status"] != "ok" {
		t.Errorf("mute = %v", ok)
	}
	asBob := must(bob, "get", channelID)
	if asBob["last_read_sequence"] != lastSeq || asBob["unread_count"] != float64(0) || asBob["muted"] != true {
		t.Errorf("bob after read+mute = %v", asBob)
	}
	if asOwner := must(owner, "get", channelID); asOwner["muted"] != false {
		t.Errorf("bob's mute reached the owner: %v", asOwner)
	}
	if ok := must(bob, "mute", channelID, "--muted=false"); ok["status"] != "ok" {
		t.Errorf("unmute = %v", ok)
	}

	// ── participants: private group roster, creator-only ───────────────────
	if ok := must(owner, "participants", "add", groupID, "room-carol"); ok["status"] != "ok" {
		t.Errorf("participants add = %v", ok)
	}
	members, _ := must(carol, "participants", "list", groupID)["participants"].([]any)
	seen := map[string]bool{}
	for _, m := range members {
		seen[str(m.(map[string]any)["user_id"])] = true
	}
	if len(members) != 3 || !seen["room-owner"] || !seen["room-bob"] || !seen["room-carol"] {
		t.Errorf("participants = %v", members)
	}
	refuse(bob, "conversation not found", "participants", "add", groupID, "room-carol")
	refuse(owner, "invalid conversation request", "participants", "add", dmID, "room-carol")
	if ok := must(owner, "participants", "remove", groupID, "room-carol"); ok["status"] != "ok" {
		t.Errorf("participants remove = %v", ok)
	}
	refuse(carol, "conversation not found", "get", groupID)

	// ── activity: channel settings, creator sets, both flags required ──────
	if a := must(bob, "activity", channelID); a["issues"] != false || a["routines"] != false {
		t.Errorf("activity default = %v", a)
	}
	if a := must(owner, "activity", channelID, "--issues=true", "--routines=false"); a["issues"] != true || a["routines"] != false {
		t.Errorf("activity set = %v", a)
	}
	refuse(bob, "conversation not found", "activity", channelID, "--issues=true", "--routines=true")
	refuse(owner, "invalid conversation request", "activity", groupID)

	// ── continue: a DM into a fresh group, retry-safe ──────────────────────
	cont := must(owner, "continue", dmID, "--kind", "group", "--title", "Planning 2", "--member", "room-carol", "--client-id", "cont-1")
	contID := str(cont["id"])
	if contID == "" || contID == dmID || cont["kind"] != "group" {
		t.Fatalf("continue = %v", cont)
	}
	if again := must(owner, "continue", dmID, "--kind", "group", "--title", "Planning 2", "--member", "room-carol", "--client-id", "cont-1"); str(again["id"]) != contID {
		t.Errorf("a retried continue made another room: %v", again)
	}
	if history, _ := must(carol, "messages", contID)["messages"].([]any); len(history) != 0 {
		t.Errorf("private DM history was copied into the continuation: %v", history)
	}

	// ── agents remove closes the loop on the channel roster ────────────────
	if ok := must(owner, "agents", "remove", channelID, agentID); ok["status"] != "ok" {
		t.Errorf("agents remove = %v", ok)
	}
	if left, _ := must(owner, "agents", "list", channelID)["agents"].([]any); len(left) != 0 {
		t.Errorf("agent still joined after remove: %v", left)
	}

	// ── the human format prints the same envelope as key/value rows ────────
	table, err := run(ownerTable, "get", groupID)
	if err != nil {
		t.Fatalf("table get: %v\n%s", err, table)
	}
	for _, cell := range []string{groupID, "title", "Planning", "kind", "group"} {
		if !strings.Contains(table, cell) {
			t.Errorf("table lacks %q:\n%s", cell, table)
		}
	}
}

// seqArg renders a message sequence — a float64 once encoding/json has been
// through it — as the integer text the --sequence flags take.
func seqArg(f float64) string { return strconv.FormatInt(int64(f), 10) }
