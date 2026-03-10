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
	key := os.Getenv("ENCRYPTION_KEY")
	if key == "" {
		fmt.Println("ENCRYPTION_KEY not set")
		os.Exit(1)
	}
	_ = key

	// Test roundtrip
	testValue := "sk-ant-api03-test-value-12345"
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
		fmt.Printf("DB key prefix: %s\n", dbDec[:min(20, len(dbDec))])
		fmt.Printf("DB key suffix: %s\n", dbDec[max(0, len(dbDec)-10):])

		// Test with Anthropic API using Bearer auth
		if strings.HasPrefix(dbDec, "sk-ant-oat") {
			fmt.Println("\nDetected OAuth token, testing with Bearer auth...")
			client := &nethttp.Client{Timeout: 10 * time.Second}
			req, _ := nethttp.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
