package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

type teamSeedFakeUser struct{ Email, Name, Role, Password, Avatar string }
type teamSeedRig struct {
	noCapability                                    bool
	server                                          *httptest.Server
	users                                           map[string]*teamSeedFakeUser
	member                                          map[string]bool
	messages                                        map[string]string
	rooms                                           map[string]bool
	provisions, resets, avatars, creates, mutations int
}

func newTeamSeedRig(t *testing.T) *teamSeedRig {
	t.Helper()
	rig := &teamSeedRig{users: map[string]*teamSeedFakeUser{"owner": {Email: "owner@example.test", Name: "Actual owner", Role: "OWNER", Avatar: "owner-avatar"}}, member: map[string]bool{"owner": true}, messages: map[string]string{}, rooms: map[string]bool{}}
	rig.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		actor := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		respond := func(status int, value any) {
			w.WriteHeader(status)
			if value != nil {
				_ = json.NewEncoder(w).Encode(value)
			}
		}
		var body map[string]any
		if r.Header.Get("Content-Type") == "application/json" {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		if r.Method != "GET" {
			rig.mutations++
		}
		switch {
		case r.URL.Path == "/openapi.json":
			if rig.noCapability {
				respond(200, map[string]any{})
				return
			}
			respond(200, map[string]any{"components": map[string]any{"schemas": map[string]any{"FinalCoreProvisionMemberRequest": map[string]any{"properties": map[string]any{"create_only": map[string]string{"type": "boolean"}}}}}})

		case r.URL.Path == "/api/auth/csrf":
			http.SetCookie(w, &http.Cookie{Name: "authjs.csrf-token", Value: "csrf"})
			respond(200, map[string]string{"csrfToken": "csrf"})
		case r.URL.Path == "/api/auth/callback/credentials":
			email, _ := body["email"].(string)
			password, _ := body["password"].(string)
			for key, user := range rig.users {
				if user.Email == email && password != "" && user.Password == password {
					http.SetCookie(w, &http.Cookie{Name: "authjs.session-token", Value: key})
					respond(200, map[string]bool{"ok": true})
					return
				}
			}
			respond(200, map[string]bool{"ok": false})
		case r.URL.Path == "/api/v1/auth/reset":
			token, _ := body["token"].(string)
			user := rig.users[strings.TrimPrefix(token, "setup-")]
			if user == nil {
				respond(400, nil)
				return
			}
			user.Password, _ = body["new_password"].(string)
			rig.resets++
			respond(200, map[string]bool{"ok": true})
		case r.URL.Path == "/api/v1/auth/cli-token/validate":
			user := rig.users[actor]
			if user == nil {
				respond(401, nil)
				return
			}
			respond(200, map[string]any{"valid": true, "user_id": actor, "user_email": user.Email})
		case r.URL.Path == "/api/v1/users/me":
			t.Error("users/me is PATCH-only and nonempty fixture profiles must not be overwritten")
			respond(405, nil)

		case strings.HasSuffix(r.URL.Path, "/members") && r.Method == "GET":
			rows := []map[string]any{}
			for key, user := range rig.users {
				if rig.member[key] {
					rows = append(rows, map[string]any{"user_id": key, "role": user.Role, "user": map[string]string{"id": key, "email": user.Email, "full_name": user.Name, "avatar_url": user.Avatar}})
				}
			}
			respond(200, rows)
		case strings.HasSuffix(r.URL.Path, "/members/provision"):
			if actor != "owner" || body["create_only"] != true {
				t.Error("provision must be owner create-only")
			}
			email, _ := body["email"].(string)
			key := strings.Split(email, ".")[0]
			if rig.users[key] != nil {
				respond(409, nil)
				return
			}
			name, _ := body["full_name"].(string)
			role, _ := body["role"].(string)
			rig.users[key] = &teamSeedFakeUser{Email: email, Name: name, Role: role}
			rig.member[key] = true
			rig.provisions++
			respond(201, map[string]any{"user_id": key, "created_user": true, "setup_url": rig.server.URL + "/reset?token=setup-" + key})
		case strings.HasSuffix(r.URL.Path, "/members") && r.Method == "POST":
			key, _ := body["user_id"].(string)
			if actor != "owner" || rig.users[key] == nil {
				respond(403, nil)
				return
			}
			rig.member[key] = true
			respond(201, map[string]bool{"ok": true})
		case r.URL.Path == "/api/v1/users/me/avatar":
			if actor == "owner" {
				t.Error("owner avatar mutated")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			rig.users[actor].Avatar = "/avatar/" + actor
			rig.avatars++
			respond(200, map[string]bool{"ok": true})
		case r.URL.Path == "/api/v1/conversations" && r.Method == "GET":
			rows := []map[string]string{}
			for id := range rig.rooms {
				rows = append(rows, map[string]string{"id": id, "title": "Team lounge · demo"})
			}
			respond(200, map[string]any{"conversations": rows, "next_offset": nil})
		case r.URL.Path == "/api/v1/conversations" && r.Method == "POST":
			rig.creates++
			id := fmt.Sprintf("channel-%d", rig.creates)
			rig.rooms[id] = true
			respond(201, map[string]string{"id": id})
		case r.URL.Path == "/api/v1/conversations/direct":
			if actor == "owner" {
				t.Error("DM must use actual human session")
			}
			respond(200, map[string]string{"id": "dm-" + actor})
		case strings.HasSuffix(r.URL.Path, "/messages"):
			id, _ := body["client_id"].(string)
			content, _ := body["content"].(string)
			key := actor + ":" + r.URL.Path + ":" + id
			if previous, exists := rig.messages[key]; exists && previous != content {
				respond(409, nil)
				return
			}
			rig.messages[key] = content
			mentions, _ := body["mentioned_agent_ids"].([]any)
			if len(mentions) != 0 {
				t.Error("seed must not dispatch agents")
			}
			respond(201, map[string]bool{"ok": true})
		case strings.HasPrefix(r.URL.Path, "/api/v1/conversations/") && r.Method == "GET":
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/conversations/")
			if !rig.rooms[id] {
				respond(404, nil)
				return
			}
			respond(200, map[string]string{"id": id, "kind": "channel", "created_by": "owner", "workspace_id": covWS})
		default:
			t.Errorf("unexpected API %s %s", r.Method, r.URL.Path)
			respond(404, nil)
		}
	}))
	t.Cleanup(rig.server.Close)
	return rig
}
func TestSeedTeamChatIndependentActorsIdempotentPrivateStateAndNoOwnerChanges(t *testing.T) {
	rig := newTeamSeedRig(t)
	client := cli.NewClient(rig.server.URL, "owner", covWS)
	dir := t.TempDir()
	result, err := seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.People) != 6 || rig.provisions != 6 || rig.resets != 6 || rig.avatars != 6 || rig.creates != 1 || len(rig.messages) != 13 {
		t.Fatalf("incomplete seed result=%+v provisions=%d resets=%d avatars=%d rooms=%d messages=%d", result, rig.provisions, rig.resets, rig.avatars, rig.creates, len(rig.messages))
	}
	for _, person := range result.People {
		if !strings.Contains(person.Email, "+") {
			t.Error("email not workspace isolated")
		}
		if rig.users[person.Key].Role != person.Role {
			t.Error("role mismatch")
		}
		if !strings.Contains(rig.messages[person.Key+":/api/v1/conversations/dm-"+person.Key+"/messages:team-chat-demo-greeting-v1"], person.Key[:1]) {
			t.Error("missing human DM")
		}
	}
	st, err := os.Stat(result.ProtectedState)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("state mode %v %v", st, err)
	}
	st, err = os.Stat(filepath.Dir(result.ProtectedState))
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatal("directory not private")
	}
	raw, err := os.ReadFile(result.ProtectedState)
	if err != nil {
		t.Fatal(err)
	}
	var state teamSeedState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	for _, a := range state.Accounts {
		if len(a.Password) != 64 || !a.SetupComplete || a.SetupToken != "" {
			t.Fatal("incomplete credential state")
		}
	}
	public, _ := json.Marshal(result)
	for _, a := range state.Accounts {
		if strings.Contains(string(public), a.Password) {
			t.Fatal("password leaked in output")
		}
	}
	rig.users["anna"].Name = "Anna personalized"
	rig.users["anna"].Avatar = "personal-avatar"
	again, err := seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.ChannelID != result.ChannelID || rig.provisions != 6 || rig.resets != 6 || rig.avatars != 6 || rig.creates != 1 || len(rig.messages) != 13 || rig.users["anna"].Name != "Anna personalized" || rig.users["anna"].Avatar != "personal-avatar" {
		t.Fatal("rerun changed owned accounts or duplicated chat")
	}
	if rig.users["owner"].Role != "OWNER" || rig.users["owner"].Name != "Actual owner" || rig.users["owner"].Avatar != "owner-avatar" {
		t.Fatal("owner changed")
	}
}
func TestSeedTeamChatRefusesUnownedOrRoleDriftBeforeMutations(t *testing.T) {
	rig := newTeamSeedRig(t)
	client := cli.NewClient(rig.server.URL, "owner", covWS)
	person := teamSeedPeople(covWS)[0]
	rig.users[person.Key] = &teamSeedFakeUser{Email: person.Email, Role: person.Role}
	rig.member[person.Key] = true
	if _, err := seedTeamChat(t.Context(), client, t.TempDir()); err == nil || !strings.Contains(err.Error(), "refusing takeover") {
		t.Fatalf("collision accepted: %v", err)
	}
	if rig.mutations != 0 {
		t.Fatal("unowned account was mutated")
	}
	delete(rig.users, person.Key)
	delete(rig.member, person.Key)
	dir := t.TempDir()
	if _, err := seedTeamChat(t.Context(), client, dir); err != nil {
		t.Fatal(err)
	}
	rig.users["peter"].Role = "ADMIN"
	before := rig.mutations
	if _, err := seedTeamChat(t.Context(), client, dir); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("role drift accepted: %v", err)
	}
	if rig.mutations != before {
		t.Fatal("role drift mutated accounts")
	}
}
func TestSeedTeamChatRecoversOwnedMembershipAndDeletedRoomWithoutPasswordReset(t *testing.T) {
	rig := newTeamSeedRig(t)
	client := cli.NewClient(rig.server.URL, "owner", covWS)
	dir := t.TempDir()
	result, err := seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	delete(rig.member, "peter")
	delete(rig.rooms, result.ChannelID)
	_, err = seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !rig.member["peter"] || rig.resets != 6 || rig.provisions != 6 || rig.creates != 2 {
		t.Fatal("owned restore reset or reprovisioned accounts")
	}
}
func TestSeedTeamChatPrivatePathRejectsGitAndSymlinks(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := teamSeedPrivateState(filepath.Join(base, "secrets"), "http://server", covWS); err == nil {
		t.Fatal("accepted Git state")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := teamSeedPrivateState(link, "http://server", covWS); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestSeedTeamChatOldServerRejectedBeforeAnyMutation(t *testing.T) {
	rig := newTeamSeedRig(t)
	rig.noCapability = true
	_, err := seedTeamChat(t.Context(), cli.NewClient(rig.server.URL, "owner", covWS), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("old server accepted: %v", err)
	}
	if rig.mutations != 0 {
		t.Fatalf("oldserver received %d mutations", rig.mutations)
	}
}

func TestSeedTeamChatFailedOwnedLoginNeverResetsPassword(t *testing.T) {
	rig := newTeamSeedRig(t)
	client := cli.NewClient(rig.server.URL, "owner", covWS)
	dir := t.TempDir()
	if _, err := seedTeamChat(t.Context(), client, dir); err != nil {
		t.Fatal(err)
	}
	rig.users["thomas"].Password = "changed by account owner"
	if _, err := seedTeamChat(t.Context(), client, dir); err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Fatalf("expected normal auth failure: %v", err)
	}
	if rig.resets != 6 || rig.provisions != 6 {
		t.Fatal("rerun reset or reprovisioned owned account")
	}
}
func TestSeedTeamChatStateLockStopsParallelMutations(t *testing.T) {
	rig := newTeamSeedRig(t)
	client := cli.NewClient(rig.server.URL, "owner", covWS)
	dir := t.TempDir()
	result, err := seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(result.ProtectedState+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	before := rig.mutations
	if _, err = seedTeamChat(t.Context(), client, dir); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("lock bypassed: %v", err)
	}
	if rig.mutations != before {
		t.Fatal("locked seed mutated server")
	}
}
func TestSeedTeamChatRecoversLostSetupAcknowledgementWithoutReset(t *testing.T) {
	rig := newTeamSeedRig(t)
	client := cli.NewClient(rig.server.URL, "owner", covWS)
	dir := t.TempDir()
	result, err := seedTeamChat(t.Context(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(result.ProtectedState)
	if err != nil {
		t.Fatal(err)
	}
	var state teamSeedState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state.Accounts["thomas"].SetupComplete = false
	state.Accounts["thomas"].SetupToken = "already-used-owned-token"
	if err = teamSeedSave(result.ProtectedState, &state); err != nil {
		t.Fatal(err)
	}
	if _, err = seedTeamChat(t.Context(), client, dir); err != nil {
		t.Fatal(err)
	}
	if rig.resets != 6 {
		t.Fatal("successful owned login still reset password")
	}
}
