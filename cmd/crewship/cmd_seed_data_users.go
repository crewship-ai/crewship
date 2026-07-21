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
// non-fatal — a user the fixture can't place is reported and the loop
// continues so the rest of the fixture lands.
//
// The flow is invite-then-signup, not signup-then-add-member. Signup
// answers 202 with a generic body whether or not the address was free
// (it was de-enumerated in #1254), so it can no longer hand back the
// new account's id — and there is deliberately no endpoint that maps an
// arbitrary email to one, because that would be the same enumeration
// oracle behind an OWNER/ADMIN gate. What exists instead is the
// invitation the server redeems inside the signup transaction: create
// the invitation for the address with the fixture role first, then sign
// the user up, and they land in this workspace already pinned. Both
// steps are idempotent, so re-seeding is a no-op.
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

	var minted []demoUser

	for _, u := range demoUsers {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Step 1: invite the address into this workspace at the fixture
		// role. 409 means either "already a member" or "an active
		// invitation is already open" — both are fine on a re-seed, and
		// both are workspace-scoped answers the caller could already get
		// from GET /members, so no cross-tenant information leaks here.
		iResp, err := client.Post(
			fmt.Sprintf("/api/v1/workspaces/%s/invitations?workspace_id=%s", wsID, wsID),
			map[string]string{"email": u.Email, "role": u.Role},
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  X %s: invite request failed: %v\n", u.Email, err)
			continue
		}
		inviteStatus := iResp.StatusCode
		if inviteStatus != http.StatusConflict {
			if err := cli.CheckError(iResp); err != nil {
				fmt.Fprintf(os.Stderr, "  X %s: invite as %s: %v\n", u.Email, u.Role, err)
				continue
			}
		} else {
			iResp.Body.Close()
		}

		// Step 2: signup. The signup handler doesn't check the caller's
		// bearer (it gates on allowSignup, not auth), so we can reuse
		// the same authenticated client. The 202 is deliberately
		// uninformative — "created" and "already exists" look the same —
		// so we don't branch on it; step 3 is what tells us whether the
		// account is actually in the workspace.
		resp, err := client.Post("/api/v1/auth/signup", map[string]string{
			"email":     u.Email,
			"full_name": u.FullName,
			"password":  u.Password,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "  X %s: signup request failed: %v\n", u.Email, err)
			continue
		}
		signupStatus := resp.StatusCode
		resp.Body.Close()
		if signupStatus != http.StatusAccepted && signupStatus != http.StatusCreated && signupStatus != http.StatusOK {
			fmt.Fprintf(os.Stderr, "  X %s: signup HTTP %d (is CREWSHIP_ALLOW_SIGNUP=true?)\n", u.Email, signupStatus)
			continue
		}

		// Step 3: verify against the roster. This is the only honest
		// signal we have — signup won't say, and the invitation is only
		// redeemed when the account was genuinely created.
		_, gotRole, lerr := findWorkspaceMemberByEmail(client, wsID, u.Email)
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "  X %s: roster lookup failed: %v\n", u.Email, lerr)
			continue
		}
		switch {
		case gotRole == "":
			// The address exists but isn't in this workspace: it had an
			// account before the seed ran, so signup was a no-op and the
			// invitation is still pending for its owner to redeem.
			fmt.Fprintf(os.Stderr, "  ↻ %s: account predates this seed; invitation left pending (re-run with --nuke for fresh state)\n", u.Email)
			continue
		case !strings.EqualFold(gotRole, u.Role):
			// No role-update endpoint for workspace members, so drift
			// from an earlier fixture is a warning, not a fix.
			fmt.Fprintf(os.Stderr, "  ! %s: existing workspace role %q ≠ fixture %q (no role-update endpoint; manual fix needed)\n", u.Email, gotRole, u.Role)
		case inviteStatus == http.StatusConflict:
			fmt.Fprintf(os.Stderr, "  ↻ %s: already in workspace with role %s; skipping\n", u.Email, u.Role)
		}
		minted = append(minted, u)
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

// findWorkspaceMemberByEmail resolves an email to (userID, currentRole)
// for users already in the current workspace's member roster. Used to
// confirm that signup actually redeemed the invitation and to read back
// the role that landed.
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
		ID    string  `json:"id"`
		Email string  `json:"email"`
		Role  *string `json:"role"`
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
