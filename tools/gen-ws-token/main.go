// gen-ws-token generates a dev WS token for testing.
// Usage: go run ./tools/gen-ws-token
// Reads NEXTAUTH_SECRET and optionally USER_ID from environment.
package main

import (
	"fmt"
	"os"
	"time"

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

	v, err := auth.NewJWTValidator(secret, "authjs.session-token")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	token, err := v.CreateToken(&auth.Claims{
		ID:    userID,
		Email: "demo@crewship.ai",
		Name:  "Demo User",
		Exp:   time.Now().Add(24 * time.Hour).Unix(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "create token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(token)
}
