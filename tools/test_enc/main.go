package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/go-resty/resty/v2"
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
			client := resty.New().
				SetTimeout(10 * time.Second).
				SetRetryCount(2).
				SetRetryWaitTime(500 * time.Millisecond).
				AddRetryCondition(func(r *resty.Response, _ error) bool {
					return r != nil && (r.StatusCode() == 429 || r.StatusCode() >= 500)
				})

			resp, err := client.R().
				SetHeader("Authorization", "Bearer "+dbDec).
				SetHeader("anthropic-version", "2023-06-01").
				Get("https://api.anthropic.com/v1/models")

			if err != nil {
				fmt.Println("Request failed:", err)
			} else {
				fmt.Printf("Status: %d\n", resp.StatusCode())
				if resp.StatusCode() != 200 {
					body := resp.String()
					if len(body) > 200 {
						body = body[:200]
					}
					fmt.Printf("Body: %s\n", body)
				} else {
					fmt.Println("SUCCESS! Token is valid.")
				}
			}
		}
	}
}
