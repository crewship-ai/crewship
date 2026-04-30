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
	secret := os.Getenv("NEXTAUTH_SECRET")
	if secret == "" {
		secret = "dev-secret-not-for-production-use-only"
	}

	userID := os.Getenv("USER_ID")
	if userID == "" {
		userID = "cmluyurk80000wnsrclcs3w40" // demo@crewship.ai from seed
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
