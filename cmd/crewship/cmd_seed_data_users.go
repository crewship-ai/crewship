package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// demoUser is the shape of one row in the seeded RBAC fixture. Roles
// are the closed set canRole() (helpers.go) gates against; passwords
// are deliberately low-entropy + documented in the printed credential
// table because this fixture only ships in dev seeds.
type demoUser struct {
	Email    string
	FullName string
	Password string
	Role     string // OWNER | ADMIN | MANAGER | MEMBER | VIEWER
}

// demoUsers is a fixed 4-row roster spanning the four non-OWNER roles
// so RBAC matrix tests have one user per role without the test author
// having to register them by hand. OWNER is whoever called `crewship
// seed` (the bootstrap admin); the four below are added underneath
// them in the same workspace.
var demoUsers = []demoUser{
	{Email: "admin2@crewship.local", FullName: "Karel Admin", Password: "adminpass1234", Role: "ADMIN"},
	{Email: "manager1@crewship.local", FullName: "Lucy Manager", Password: "managerpass12", Role: "MANAGER"},
	{Email: "member1@crewship.local", FullName: "Tom Member", Password: "memberpass12", Role: "MEMBER"},
	{Email: "viewer1@crewship.local", FullName: "Ivana Viewer", Password: "viewerpass12", Role: "VIEWER"},
}

// seedRBACUsers provisions new accounts through the authenticated workspace
// administration API. Only newly created accounts redeem their setup token;
// existing accounts require proof of their documented fixture credentials and
// are never assigned a new password. Public signup need not be enabled.
func seedRBACUsers(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client = client.WithContext(ctx)
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return fmt.Errorf("seedRBACUsers: workspace_id not set on client")
	}
	fmt.Fprintln(os.Stderr, "Seeding RBAC fixture through authenticated member provisioning...")
	placed := 0
	for _, u := range demoUsers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := provisionSeedUser(ctx, client, wsID, u); err != nil {
			fmt.Fprintf(os.Stderr, "  X %s: %v\n", u.Email, err)
			continue
		}
		_, role, err := findWorkspaceMemberByEmail(client, wsID, u.Email)
		if err != nil || !strings.EqualFold(role, u.Role) {
			fmt.Fprintf(os.Stderr, "  X %s: fixture role could not be verified (existing roles and passwords preserved)\n", u.Email)
			continue
		}
		placed++
		fmt.Fprintf(os.Stderr, "  ✓ %s: %s\n", u.Email, u.Role)
	}
	if placed != len(demoUsers) {
		return fmt.Errorf("incomplete RBAC fixture (%d of %d): see per-user failures; existing passwords were never reset", placed, len(demoUsers))
	}
	fmt.Fprintln(os.Stderr, "RBAC fixture ready. New accounts use the documented demo credentials; existing accounts retain their passwords.")
	return nil
}

func provisionSeedUser(ctx context.Context, client *cli.Client, wsID string, u demoUser) error {
	resp, err := client.Post("/api/v1/workspaces/"+url.PathEscape(wsID)+"/members/provision", map[string]any{
		"email": u.Email, "full_name": u.FullName, "role": u.Role, "create_only": true,
	})
	if err != nil {
		return fmt.Errorf("provision request failed: %w", err)
	}
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		// Even existing members must prove the fixture login still works.
		// Member-add is idempotent and never resets passwords or roles.
		return placeExistingSeedUser(ctx, client, wsID, u)
	}
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("provision failed: %w", err)
	}
	var result struct {
		UserID      string `json:"user_id" yaml:"user_id"`
		CreatedUser bool   `json:"created_user" yaml:"created_user"`
		SetupURL    string `json:"setup_url" yaml:"setup_url"`
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return fmt.Errorf("read provision response: %w", err)
	}
	if !result.CreatedUser || result.UserID == "" {
		return fmt.Errorf("server did not confirm creation of a new account; no password was changed")
	}
	link, err := url.Parse(result.SetupURL)
	if err != nil || link.Query().Get("token") == "" {
		return fmt.Errorf("server did not return an account setup token")
	}
	// Never navigate the returned URL: redeem only against the explicit seed
	// target. The URL's hostname cannot redirect credentials to a third party.
	resp, err = client.Post("/api/v1/auth/reset", map[string]string{"token": link.Query().Get("token"), "new_password": u.Password})
	if err != nil {
		return fmt.Errorf("new account setup failed: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("new account setup failed: %w", err)
	}
	return nil
}

// placeExistingSeedUser obtains an ID only through authenticated self identity,
// never a global email lookup. Login does not save CLI configuration or mint a
// persistent CLI token, and the original account/password remain unchanged.
func placeExistingSeedUser(ctx context.Context, owner *cli.Client, wsID string, user demoUser) error {
	login := cli.NewClient(owner.BaseURL, "", "").WithContext(ctx)
	session, err := exchangeCredentialsForSession(login, owner.BaseURL, user.Email, user.Password)
	if err != nil {
		return fmt.Errorf("authenticate documented fixture credentials: %w", err)
	}
	self := cli.NewClient(owner.BaseURL, session, "").WithContext(ctx)
	defer closeTransientSeedSession(ctx, self, session)
	resp, err := self.Get("/api/v1/auth/cli-token/validate")
	if err != nil {
		return fmt.Errorf("read authenticated account identity: %w", err)
	}
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("read authenticated account identity: %w", err)
	}
	var identity struct {
		UserID string `json:"user_id" yaml:"user_id"`
		Email  string `json:"user_email" yaml:"user_email"`
	}
	if err := cli.ReadJSON(resp, &identity); err != nil {
		return fmt.Errorf("read authenticated account identity: %w", err)
	}
	if identity.UserID == "" || !strings.EqualFold(identity.Email, user.Email) {
		return fmt.Errorf("authenticated account identity does not match fixture email")
	}
	resp, err = owner.Post(fmt.Sprintf("/api/v1/workspaces/%s/members", wsID), map[string]string{"user_id": identity.UserID, "role": user.Role})
	if err != nil {
		return fmt.Errorf("place authenticated fixture account: %w", err)
	}
	// A concurrent seed may have already placed it. The caller always reads
	// back the actual workspace roster rather than trusting this status.
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return nil
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

func closeTransientSeedSession(ctx context.Context, self *cli.Client, session string) {
	// Cancellation must stop seed work, while still allowing a bounded attempt
	// to close only the login session it created. Other sessions stay active.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	logout := self.WithContext(cleanupCtx).WithHeader("Cookie", "authjs.session-token="+session+"; __Secure-authjs.session-token="+session)
	resp, err := logout.Post("/api/auth/signout", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "  ! Could not close transient fixture login session")
		return
	}
	defer resp.Body.Close()
	if cli.CheckError(resp) != nil {
		fmt.Fprintln(os.Stderr, "  ! Could not close transient fixture login session")
	}
}

// findWorkspaceMemberByEmail resolves an email to (userID, currentRole)
// for users already in the current workspace's member roster. Used to
// verify provisioning and to read back the role that landed.
//
// Returns ("", "", nil) when the email isn't found in this workspace —
// the user may exist in the global users table but not be a member here,
// in which case the seed loop logs and skips them (the OWNER-only admin
// endpoint we hit here can't see cross-workspace users).
func findWorkspaceMemberByEmail(client *cli.Client, wsID, email string) (userID, role string, err error) {
	resp, err := client.Get(fmt.Sprintf("/api/v1/admin/users?workspace_id=%s", wsID))
	if err != nil {
		return "", "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", "", err
	}
	var rows []struct {
		ID    string  `json:"id" yaml:"id"`
		Email string  `json:"email" yaml:"email"`
		Role  *string `json:"role" yaml:"role"`
	}
	if err := cli.ReadJSON(resp, &rows); err != nil {
		return "", "", err
	}
	for _, r := range rows {
		if strings.EqualFold(r.Email, email) {
			cur := ""
			if r.Role != nil {
				cur = *r.Role
			}
			return r.ID, cur, nil
		}
	}
	return "", "", nil
}
