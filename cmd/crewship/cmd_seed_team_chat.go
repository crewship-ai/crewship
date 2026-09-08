package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type teamSeedAccount struct {
	Email         string `json:"email"`
	Password      string `json:"password"`
	UserID        string `json:"user_id,omitempty"`
	SetupToken    string `json:"setup_token,omitempty"`
	SetupComplete bool   `json:"setup_complete"`
	DirectID      string `json:"direct_conversation_id,omitempty"`
}
type teamSeedState struct {
	Server         string                      `json:"server"`
	WorkspaceID    string                      `json:"workspace_id"`
	OwnerID        string                      `json:"owner_id"`
	ChannelID      string                      `json:"channel_id,omitempty"`
	ChannelPending bool                        `json:"channel_pending,omitempty"`
	Accounts       map[string]*teamSeedAccount `json:"accounts"`
}
type teamSeedPersonResult struct {
	Key      string `json:"key"`
	Email    string `json:"email"`
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
	DirectID string `json:"direct_conversation_id"`
}
type teamSeedResult struct {
	WorkspaceID    string                 `json:"workspace_id"`
	ChannelID      string                 `json:"channel_id"`
	ProtectedState string                 `json:"protected_state"`
	People         []teamSeedPersonResult `json:"people"`
}

func newSeedTeamChatCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "team-chat", Short: "Add fictional colleagues, local avatars and human Chat examples without resetting the workspace", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		dir, _ := cmd.Flags().GetString("state-dir")
		result, err := seedTeamChat(cmd.Context(), newAPIClient(), dir)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}}
	cmd.Flags().String("state-dir", "", "Private credential state directory outside Git (default: user config directory; isolated per server/workspace)")
	return cmd
}

// Never return server response bodies here: setup/login errors may echo secrets.
type teamSeedStatusError struct {
	method, path string
	status       int
}

func (e *teamSeedStatusError) Error() string {
	return fmt.Sprintf("team-chat API %s %s returned HTTP %d", e.method, e.path, e.status)
}
func teamSeedJSON(client *cli.Client, method, path string, body, out any) error {
	response, err := client.Do(method, path, body)
	if err != nil {
		return fmt.Errorf("team-chat API request failed (%s %s); sensitive details suppressed", method, strings.Split(path, "?")[0])
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &teamSeedStatusError{method: method, path: strings.Split(path, "?")[0], status: response.StatusCode}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("team-chat API returned invalid JSON")
	}
	return nil
}

type teamSeedMember struct {
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
	Email     string `json:"email"`
	FullName  string `json:"full_name"`
	AvatarURL string `json:"avatar_url"`
	User      *struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		FullName  string `json:"full_name"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
}

func (m *teamSeedMember) email() string {
	if m.User != nil {
		return m.User.Email
	}
	return m.Email
}
func (m *teamSeedMember) name() string {
	if m.User != nil {
		return m.User.FullName
	}
	return m.FullName
}
func (m *teamSeedMember) avatar() string {
	if m.User != nil {
		return m.User.AvatarURL
	}
	return m.AvatarURL
}
func teamSeedMembers(owner *cli.Client, w string) ([]teamSeedMember, error) {
	var rows []teamSeedMember
	err := teamSeedJSON(owner, http.MethodGet, "/api/v1/workspaces/"+url.PathEscape(w)+"/members", nil, &rows)
	return rows, err
}
func teamSeedFindMember(rows []teamSeedMember, email string) *teamSeedMember {
	for i := range rows {
		if strings.EqualFold(rows[i].email(), email) {
			return &rows[i]
		}
	}
	return nil
}

// The explicit directory must be private and outside any Git worktree. Use a
// per-instance workspace digest so different checkouts cannot share credentials.
func teamSeedPrivateState(base, server, w string) (string, error) {
	if base == "" {
		home, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "crewship", "team-chat")
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	for p := absolute; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return "", fmt.Errorf("team-chat credential state must be outside Git")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	// Reject symlink components, including an existing ancestor of a new path.
	for p := absolute; ; p = filepath.Dir(p) {
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("team-chat state cannot use symlinks")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(server + "\n" + w))
	scoped := filepath.Join(absolute, hex.EncodeToString(sum[:12]))
	if st, err := os.Lstat(scoped); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("team-chat state cannot use symlinks")
	}
	if err = os.MkdirAll(scoped, 0700); err != nil {
		return "", err
	}
	if err = os.Chmod(scoped, 0700); err != nil {
		return "", err
	}
	return filepath.Join(scoped, "accounts.json"), nil
}
func teamSeedSave(path string, state *teamSeedState) error {
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("team-chat state must be a regular private file")
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".accounts-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func teamSeedLogin(ctx context.Context, owner *cli.Client, account *teamSeedAccount) (*cli.Client, error) {
	anonymous := teamSeedAuthClient(ctx, owner.BaseURL)
	token, err := exchangeCredentialsForSession(anonymous, owner.BaseURL, account.Email, account.Password)
	if err != nil {
		return nil, fmt.Errorf("team-chat account login failed; no password changes attempted")
	}
	return cli.NewClient(owner.BaseURL, token, owner.WorkspaceID).WithContext(ctx).WithTimeout(30 * time.Second), nil
}
func teamSeedAvatar(ctx context.Context, client *cli.Client, key string) error {
	image, err := seeddata.TeamChatAvatar(key)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", key+".png")
	if err != nil {
		return err
	}
	if _, err = file.Write(image); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	req, err := client.NewRequest(ctx, http.MethodPost, "/api/v1/users/me/avatar", &body)
	if err != nil {
		return fmt.Errorf("team-chat avatar request failed")
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := client.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("team-chat avatar upload failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("team-chat avatar upload HTTP %d", response.StatusCode)
	}
	return nil
}

func seedTeamChat(ctx context.Context, caller *cli.Client, base string) (teamSeedResult, error) {
	result := teamSeedResult{}
	owner := caller.WithContext(ctx).WithTimeout(30 * time.Second)
	owner.Verbose = false
	parsed, err := url.Parse(owner.BaseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return result, fmt.Errorf("team-chat requires a plain HTTP(S) server origin")
	}
	server := strings.TrimRight(owner.BaseURL, "/")
	owner.BaseURL = server
	w := owner.GetWorkspaceID()
	if w == "" {
		return result, fmt.Errorf("team-chat requires an existing workspace")
	}
	people := teamSeedPeople(w)
	var me struct {
		ID    string `json:"user_id"`
		Email string `json:"user_email"`
	}
	if err = teamSeedJSON(owner, http.MethodGet, "/api/v1/auth/cli-token/validate", nil, &me); err != nil {
		return result, err
	}
	members, err := teamSeedMembers(owner, w)
	if err != nil {
		return result, err
	}
	ownerMember := teamSeedFindMember(members, me.Email)
	if ownerMember == nil || ownerMember.UserID != me.ID || ownerMember.Role != "OWNER" {
		return result, fmt.Errorf("team-chat seed requires the current workspace OWNER; ownership is never changed")
	}
	if err = requireCreateOnlyProvision(owner); err != nil {
		return result, err
	}
	path, err := teamSeedPrivateState(base, server, w)
	if err != nil {
		return result, err
	}
	// Exclusive create makes simultaneous seed invocations fail before mutation.
	// A stale lock after a process crash is deliberately not silently stolen.
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, fmt.Errorf("team-chat state is locked; check %s.lock before retrying", path)
	}
	lock.Close()
	defer os.Remove(path + ".lock")
	state := teamSeedState{Server: server, WorkspaceID: w, OwnerID: me.ID, Accounts: map[string]*teamSeedAccount{}}
	if st, err := os.Lstat(path); err == nil {
		if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
			return result, fmt.Errorf("team-chat state must be a regular 0600 file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return result, err
		}
		if err = json.Unmarshal(raw, &state); err != nil {
			return result, fmt.Errorf("team-chat private state is invalid")
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if state.Server != server || state.WorkspaceID != w || state.OwnerID != me.ID || state.Accounts == nil {
		return result, fmt.Errorf("team-chat private state identity does not match this owner/server/workspace")
	}
	// Check every known collision/role drift before provisioning any new account.
	for _, person := range people {
		existing := teamSeedFindMember(members, person.Email)
		account := state.Accounts[person.Key]
		if existing != nil && (account == nil || account.UserID == "" || existing.UserID != account.UserID) {
			return result, fmt.Errorf("%s already exists without locally owned credentials; refusing takeover", person.Key)
		}
		if existing != nil && existing.Role != person.Role {
			return result, fmt.Errorf("%s role differs from the catalog; refusing to change permissions", person.Key)
		}
		if account != nil && (account.Email != person.Email || len(account.Password) < 40) {
			return result, fmt.Errorf("%s saved account identity is invalid; refusing reprovision", person.Key)
		}
	}
	actors := map[string]*cli.Client{}
	for _, person := range people {
		account := state.Accounts[person.Key]
		if account == nil {
			secret := make([]byte, 32)
			if _, err = rand.Read(secret); err != nil {
				return result, err
			}
			account = &teamSeedAccount{Email: person.Email, Password: hex.EncodeToString(secret)}
			state.Accounts[person.Key] = account
			if err = teamSeedSave(path, &state); err != nil {
				return result, err
			}
		}
		restoreMembership := account.UserID != "" && teamSeedFindMember(members, person.Email) == nil
		justProvisioned := false
		if account.UserID == "" {
			var provision struct {
				UserID   string `json:"user_id"`
				Created  bool   `json:"created_user"`
				SetupURL string `json:"setup_url"`
			}
			if err = teamSeedJSON(owner, http.MethodPost, "/api/v1/workspaces/"+url.PathEscape(w)+"/members/provision", map[string]any{"email": person.Email, "full_name": person.FullName, "role": person.Role, "create_only": true}, &provision); err != nil {
				return result, fmt.Errorf("%s: %w", person.Key, err)
			}
			if !provision.Created || provision.UserID == "" || provision.SetupURL == "" {
				return result, fmt.Errorf("%s is not a newly owned account; refusing setup", person.Key)
			}
			setup, err := url.Parse(provision.SetupURL)
			if err != nil || setup.Query().Get("token") == "" {
				return result, fmt.Errorf("%s setup token missing", person.Key)
			}
			justProvisioned = true
			account.UserID = provision.UserID
			account.SetupToken = setup.Query().Get("token")
			if err = teamSeedSave(path, &state); err != nil {
				return result, err
			}
		}
		var actor *cli.Client
		if account.SetupComplete {
			actor, err = teamSeedLogin(ctx, owner, account)
			if err != nil {
				return result, fmt.Errorf("%s: %w", person.Key, err)
			}
		} else {
			// A successful login recovers a reset whose response/state save was lost.
			if justProvisioned {
				err = fmt.Errorf("new account needs setup")
			} else {
				actor, err = teamSeedLogin(ctx, owner, account)
			}
			if err != nil {
				if account.SetupToken == "" {
					return result, fmt.Errorf("%s lacks its owned setup token", person.Key)
				}
				anonymous := teamSeedAuthClient(ctx, server)
				if err = teamSeedJSON(anonymous, http.MethodPost, "/api/v1/auth/reset", map[string]string{"token": account.SetupToken, "new_password": account.Password}, nil); err != nil {
					return result, fmt.Errorf("%s setup failed; saved credentials unchanged", person.Key)
				}
				actor, err = teamSeedLogin(ctx, owner, account)
				if err != nil {
					return result, fmt.Errorf("%s: %w", person.Key, err)
				}
			}
			account.SetupComplete = true
			account.SetupToken = ""
			if err = teamSeedSave(path, &state); err != nil {
				return result, err
			}
		}
		var identity struct {
			ID    string `json:"user_id"`
			Email string `json:"user_email"`
		}
		if err = teamSeedJSON(actor, http.MethodGet, "/api/v1/auth/cli-token/validate", nil, &identity); err != nil {
			return result, err
		}
		if identity.ID != account.UserID || !strings.EqualFold(identity.Email, account.Email) {
			return result, fmt.Errorf("%s login identity mismatch", person.Key)
		}
		if restoreMembership {
			if err = teamSeedJSON(owner, http.MethodPost, "/api/v1/workspaces/"+url.PathEscape(w)+"/members", map[string]string{"user_id": account.UserID, "role": person.Role}, nil); err != nil {
				return result, err
			}
		}
		// The real profile read is the owner-visible roster; users/me is PATCH-only.
		roster, err := teamSeedMembers(owner, w)
		if err != nil {
			return result, err
		}
		profile := teamSeedFindMember(roster, person.Email)
		if profile == nil || profile.UserID != account.UserID || profile.Role != person.Role {
			return result, fmt.Errorf("%s roster identity or role mismatch", person.Key)
		}
		if profile.name() == "" {
			if err = teamSeedJSON(actor, http.MethodPatch, "/api/v1/users/me", map[string]string{"full_name": person.FullName}, nil); err != nil {
				return result, err
			}
		}
		if profile.avatar() == "" {
			if err = teamSeedAvatar(ctx, actor, person.Key); err != nil {
				return result, err
			}
		}
		actors[person.Key] = actor
	}
	// Confirm all actual roles before putting any seeded conversation in the inbox.
	members, err = teamSeedMembers(owner, w)
	if err != nil {
		return result, err
	}
	for _, person := range people {
		m := teamSeedFindMember(members, person.Email)
		if m == nil || m.UserID != state.Accounts[person.Key].UserID || m.Role != person.Role {
			return result, fmt.Errorf("%s expected role was not provisioned", person.Key)
		}
	}
	if state.ChannelID != "" {
		var previous struct {
			ID string `json:"id"`
		}
		lookupErr := teamSeedJSON(owner, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(state.ChannelID), nil, &previous)
		if lookupErr != nil {
			var status *teamSeedStatusError
			if !errors.As(lookupErr, &status) || status.status != 404 {
				return result, lookupErr
			}
			state.ChannelID = ""
			state.ChannelPending = false
			if err = teamSeedSave(path, &state); err != nil {
				return result, err
			}
		}
	}
	if state.ChannelID == "" {
		if state.ChannelPending {
			return result, fmt.Errorf("team-chat channel creation response was lost; inspect owned state before retrying to avoid duplicates")
		}
		// Do not adopt an unrelated room that happens to have the demo title.
		for offset := 0; ; offset += 100 {
			var page struct {
				Rooms []struct {
					Title string `json:"title"`
				} `json:"conversations"`
				Next *int `json:"next_offset"`
			}
			if err = teamSeedJSON(owner, http.MethodGet, fmt.Sprintf("/api/v1/conversations?limit=100&offset=%d", offset), nil, &page); err != nil {
				return result, err
			}
			for _, room := range page.Rooms {
				if room.Title == "Team lounge · demo" {
					return result, fmt.Errorf("Team lounge demo already exists without this seed's owned state")
				}
			}
			if page.Next == nil {
				break
			}
		}
		state.ChannelPending = true
		if err = teamSeedSave(path, &state); err != nil {
			return result, err
		}
		var room struct {
			ID string `json:"id"`
		}
		if err = teamSeedJSON(owner, http.MethodPost, "/api/v1/conversations", map[string]any{"title": "Team lounge · demo", "kind": "channel", "member_ids": []string{}}, &room); err != nil {
			var status *teamSeedStatusError
			if errors.As(err, &status) {
				switch status.status {
				case 400, 401, 403, 404, 409, 422, 429:
					state.ChannelPending = false
					if saveErr := teamSeedSave(path, &state); saveErr != nil {
						return result, saveErr
					}
				}
			}
			return result, err
		}
		if room.ID == "" {
			return result, fmt.Errorf("team-chat channel creation returned no ID")
		}
		state.ChannelID = room.ID
		state.ChannelPending = false
		if err = teamSeedSave(path, &state); err != nil {
			return result, err
		}
	}
	var existingRoom struct {
		Kind      string `json:"kind"`
		Owner     string `json:"created_by"`
		Workspace string `json:"workspace_id"`
	}
	if err = teamSeedJSON(owner, http.MethodGet, "/api/v1/conversations/"+url.PathEscape(state.ChannelID), nil, &existingRoom); err != nil {
		return result, err
	}
	if existingRoom.Kind != "channel" || existingRoom.Owner != me.ID || existingRoom.Workspace != w {
		return result, fmt.Errorf("owned demo channel identity changed")
	}
	send := func(actor *cli.Client, room, id, content string) error {
		return teamSeedJSON(actor, http.MethodPost, "/api/v1/conversations/"+url.PathEscape(room)+"/messages", map[string]any{"client_id": id, "content": content, "mentioned_agent_ids": []string{}}, nil)
	}
	if err = send(owner, state.ChannelID, "team-chat-demo-owner-v1", "DEMO: This channel contains fictional colleagues and example planning messages. No real tasks or agent work are created. Workspace roles are real; OWNER remains unchanged."); err != nil {
		return result, err
	}
	for _, person := range people {
		account := state.Accounts[person.Key]
		actor := actors[person.Key]
		var direct struct {
			ID string `json:"id"`
		}
		if err = teamSeedJSON(actor, http.MethodPost, "/api/v1/conversations/direct", map[string]string{"user_id": me.ID}, &direct); err != nil {
			return result, err
		}
		account.DirectID = direct.ID
		if err = teamSeedSave(path, &state); err != nil {
			return result, err
		}
		if err = send(actor, direct.ID, "team-chat-demo-greeting-v1", "Hi! I’m "+person.FullName+", a fictional "+person.JobTitle+" for this Chat demonstration. This is an example direct message, not a real work request."); err != nil {
			return result, err
		}
		if err = send(actor, state.ChannelID, "team-chat-demo-planning-v1", person.Message); err != nil {
			return result, err
		}
		result.People = append(result.People, teamSeedPersonResult{Key: person.Key, Email: person.Email, UserID: account.UserID, Role: person.Role, DirectID: direct.ID})
	}
	result.WorkspaceID = w
	result.ChannelID = state.ChannelID
	result.ProtectedState = path
	return result, nil
}

// Accounts are global, while each demo workspace needs independent owned users.
func teamSeedPeople(workspace string) []seeddata.TeamChatPerson {
	sum := sha256.Sum256([]byte(workspace))
	suffix := hex.EncodeToString(sum[:4])
	people := append([]seeddata.TeamChatPerson(nil), seeddata.TeamChatPeople...)
	for i := range people {
		local, domain, ok := strings.Cut(people[i].Email, "@")
		if ok {
			people[i].Email = local + "+" + suffix + "@" + domain
		}
	}
	return people
}

// Fail closed before the first provision: old servers ignore unknown JSON fields.
func requireCreateOnlyProvision(client *cli.Client) error {
	response, err := client.Get("/openapi.json")
	if err != nil {
		return fmt.Errorf("server create-only provisioning support could not be verified; upgrade the server before retrying")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server does not advertise create-only provisioning; upgrade the server before retrying")
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&spec); err != nil || spec.Components.Schemas["FinalCoreProvisionMemberRequest"].Properties["create_only"].Type != "boolean" {
		return fmt.Errorf("server does not advertise create-only provisioning; upgrade the server before retrying")
	}
	return nil
}
