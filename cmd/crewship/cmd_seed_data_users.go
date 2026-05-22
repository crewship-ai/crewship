package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

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

// seedRBACUsers signs up the four demoUsers and pins each to its
// target role on the workspace. After the seed completes, each user
// can log in with `crewship login --token` after running
// `crewship login` (interactive) with their seeded email/password
// to mint their own CLI token. Seeds do NOT mint tokens for these
// users because the existing POST /api/v1/auth/cli-token requires
// session auth as the target user; adding a sideways admin path is
// out of scope for the seed enhancement.
//
// Toggled via `--with-users`. Default off so existing seeds stay
// byte-identical and operators don't get four extra entries in their
// admin user list without asking.
//
// Requires CREWSHIP_ALLOW_SIGNUP=true on the server (signup endpoint
// gates on this; bootstrap is the only path otherwise). Errors are
// non-fatal — a single signup that 409s (user already exists) is
// reported and the loop continues so the rest of the fixture lands.
func seedRBACUsers(ctx context.Context, client *cli.Client) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return fmt.Errorf("seedRBACUsers: workspace_id not set on client")
	}
	fmt.Fprintln(os.Stderr, "Seeding RBAC fixture (4 users × 4 roles)...")
	fmt.Fprintln(os.Stderr, "  (requires CREWSHIP_ALLOW_SIGNUP=true on server)")

	type row struct {
		demoUser
		UserID string
	}
	var minted []row

	for _, u := range demoUsers {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Step 1: signup. The signup handler doesn't check the caller's
		// bearer (it gates on allowSignup, not auth), so we can reuse
		// the same authenticated client. The signed-up user's session
		// cookies are returned in the response but ignored — they
		// would expire in 15 min anyway and the seed has no use for
		// them.
		resp, err := client.Post("/api/v1/auth/signup", map[string]string{
			"email":     u.Email,
			"full_name": u.FullName,
			"password":  u.Password,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  X %s: signup request failed: %v\n", u.Email, err)
			continue
		}
		if resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "  ↻ %s: already exists, skipping signup\n", u.Email)
			continue
		}
		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			body, _ := readBody(resp)
			fmt.Fprintf(os.Stderr, "  X %s: signup HTTP %d: %s\n", u.Email, resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}
		var signup struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}
		if err := cli.ReadJSON(resp, &signup); err != nil {
			fmt.Fprintf(os.Stderr, "  X %s: signup parse: %v\n", u.Email, err)
			continue
		}

		// Step 2: pin role via workspace_members. The bootstrap admin
		// (already authed as client) is OWNER, so this call succeeds
		// even when the target role is ADMIN or higher.
		mResp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/members?workspace_id=%s", wsID, wsID),
			map[string]interface{}{"user_id": signup.ID, "role": u.Role},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  X %s: assign %s: %v\n", u.Email, u.Role, err)
			continue
		}
		if err := cli.CheckError(mResp); err != nil {
			fmt.Fprintf(os.Stderr, "  X %s: assign %s: %v\n", u.Email, u.Role, err)
			continue
		}
		minted = append(minted, row{demoUser: u, UserID: signup.ID})
	}

	if len(minted) == 0 {
		fmt.Fprintln(os.Stderr, "  (no users seeded; --with-users requires CREWSHIP_ALLOW_SIGNUP=true)")
		return nil
	}

	// Print the credential table to stderr so the operator can copy
	// values into a login command without re-reading the DB. Stderr
	// (not stdout) so any future JSON seed output stays parseable.
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "RBAC fixture credentials (dev only — never use these in production):")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "  %-26s  %-8s  %s\n", "EMAIL", "ROLE", "PASSWORD")
	fmt.Fprintln(os.Stderr, "  "+strings.Repeat("─", 64))
	for _, r := range minted {
		fmt.Fprintf(os.Stderr, "  %-26s  %-8s  %s\n", r.Email, r.Role, r.Password)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  Each user can mint a CLI token with:")
	fmt.Fprintln(os.Stderr, "    crewship login  # interactive prompt for email + password above")
	fmt.Fprintln(os.Stderr, "")
	return nil
}
