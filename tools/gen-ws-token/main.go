// gen-ws-token generates a dev WS ticket for testing.
// Usage: go run ./tools/gen-ws-token
// Reads NEXTAUTH_SECRET, USER_ID, SESSION_ID from the environment.
//
// SESSION_ID must reference a row in user_sessions; the auth middleware
// rejects tickets whose sid doesn't resolve to an active session, so a
// fresh signup or seed user is the easiest source. Run with `seed`
// already applied or insert a row manually.
package main

import (
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/auth"
)

func main() {
	// Fail fast on missing secret. Generating a token against the
	// hardcoded dev fallback used to silently produce a JWE the real
	// server immediately rejects, which made the failure mode "WS
	// upgrade returns 401 with no useful error" — confusing during
	// debugging. Mirror the existing SESSION_ID handling.
	secret := os.Getenv("NEXTAUTH_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "NEXTAUTH_SECRET required (read from /etc/crewship/*.env on server hosts)")
		os.Exit(2)
	}

	// USER_ID and SESSION_ID must agree — the WS hub validates the
	// ticket's claims.id matches user_sessions.user_id for the
	// claims.sid row. Falling back to a hardcoded seed user can
	// produce a token whose subject and session belong to different
	// users; that gets rejected on first use, but it makes "why
	// is my dev WS not connecting" a confusing debugging trail.
	// Require both explicitly.
	userID := os.Getenv("USER_ID")
	if userID == "" {
		fmt.Fprintln(os.Stderr, "USER_ID required — must match the user_id of the SESSION_ID row in user_sessions")
		os.Exit(2)
	}

	sessionID := os.Getenv("SESSION_ID")
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "SESSION_ID required — pick a row from user_sessions")
		os.Exit(2)
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	token, err := v.IssueWSTicket(userID, sessionID, "Demo User", "demo@crewship.ai")
	if err != nil {
		fmt.Fprintf(os.Stderr, "issue ws ticket: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(token)
}
