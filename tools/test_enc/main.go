package main

import (
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func main() {
	if os.Getenv("ENCRYPTION_KEY") == "" {
		fmt.Println("ENCRYPTION_KEY not set")
		os.Exit(1)
	}

	// Test roundtrip
	testValue := "test-credential-roundtrip-value-12345"
	enc, err := encryption.Encrypt(testValue)
	if err != nil {
		fmt.Println("Encrypt error:", err)
		os.Exit(1)
	}
	dec, err := encryption.Decrypt(enc)
	if err != nil {
		fmt.Println("Decrypt error:", err)
		os.Exit(1)
	}
	fmt.Printf("Roundtrip match: %v\n", testValue == dec)

	// Now decrypt the actual DB value
	if len(os.Args) > 1 {
		dbEnc := os.Args[1]
		dbDec, err := encryption.Decrypt(dbEnc)
		if err != nil {
			fmt.Println("DB decrypt error:", err)
			os.Exit(1)
		}
		fmt.Printf("DB key length: %d\n", len(dbDec))
		switch {
		case strings.HasPrefix(dbDec, "sk-ant-oat"):
			fmt.Println("DB token type: anthropic-oauth")
		case strings.HasPrefix(dbDec, "sk-ant-"):
			fmt.Println("DB token type: anthropic-api-key")
		case strings.HasPrefix(dbDec, "glpat-"):
			fmt.Println("DB token type: gitlab-pat")
		default:
			fmt.Println("DB credential: ***masked***")
		}

		// Test with Anthropic API using Bearer auth
		if strings.HasPrefix(dbDec, "sk-ant-oat") {
			fmt.Println("\nDetected OAuth token, testing with Bearer auth...")
			client := &nethttp.Client{Timeout: 10 * time.Second}
			req, err := nethttp.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
			if err != nil {
				fmt.Println("Failed to create request:", err)
				os.Exit(1)
			}
			req.Header.Set("Authorization", "Bearer "+dbDec)
			req.Header.Set("anthropic-version", "2023-06-01")
			resp, err := client.Do(req)
			if err != nil {
				fmt.Println("Request failed:", err)
			} else {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				fmt.Printf("Status: %d\n", resp.StatusCode)
				if resp.StatusCode != 200 {
					fmt.Printf("Body: %s\n", string(body[:min(200, len(body))]))
				} else {
					fmt.Println("SUCCESS! Token is valid.")
				}
			}
		}
	}
}
