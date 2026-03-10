package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
)

func main() {
	cmd := "help"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "generate-keypair":
		generateKeypair()
	case "sign":
		signLicense()
	default:
		fmt.Fprintf(os.Stderr, "Usage: license-keygen <command>\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  generate-keypair   Generate a new Ed25519 keypair\n")
		fmt.Fprintf(os.Stderr, "  sign               Sign a license file\n")
		os.Exit(1)
	}
}

func generateKeypair() {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	pubB64 := base64.StdEncoding.EncodeToString(pub)
	privB64 := base64.StdEncoding.EncodeToString(priv)

	fmt.Printf("Public key (embed in binary via ldflags):\n  %s\n\n", pubB64)
	fmt.Printf("Private key (keep SECRET, use for signing):\n  %s\n\n", privB64)
	fmt.Printf("Makefile ldflags example:\n")
	fmt.Printf("  -X github.com/crewship-ai/crewship/internal/license.publicKey=%s\n", pubB64)
}

func signLicense() {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	privKeyStr := fs.String("key", "", "Base64-encoded Ed25519 private key")
	licenseID := fs.String("id", "", "License ID")
	name := fs.String("name", "", "Licensee name")
	org := fs.String("org", "", "Licensee organization")
	edition := fs.String("edition", "team", "Edition: community, team, enterprise")
	maxCrews := fs.Int("max-crews", 50, "Maximum crews")
	maxAgents := fs.Int("max-agents", 25, "Maximum agents per crew")
	maxMembers := fs.Int("max-members", 25, "Maximum workspace members")
	features := fs.String("features", "", "Comma-separated feature flags")
	validDays := fs.Int("valid-days", 365, "License validity in days")
	output := fs.String("out", "license.json", "Output file path")

	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(1)
	}

	if *privKeyStr == "" || *licenseID == "" || *name == "" {
		fmt.Fprintf(os.Stderr, "Required: --key, --id, --name\n")
		fs.Usage()
		os.Exit(1)
	}

	privKeyBytes, err := base64.StdEncoding.DecodeString(*privKeyStr)
	if err != nil || len(privKeyBytes) != ed25519.PrivateKeySize {
		fmt.Fprintf(os.Stderr, "Invalid private key\n")
		os.Exit(1)
	}

	now := time.Now()
	claims := license.Claims{
		LicenseID:    *licenseID,
		LicenseeName: *name,
		LicenseeOrg:  *org,
		Edition:      license.Edition(*edition),
		MaxCrews:     *maxCrews,
		MaxAgents:    *maxAgents,
		MaxMembers:   *maxMembers,
		IssuedAt:     now.Unix(),
		ExpiresAt:    now.Add(time.Duration(*validDays) * 24 * time.Hour).Unix(),
	}

	if *features != "" {
		var featureList []string
		for _, f := range splitAndTrim(*features) {
			featureList = append(featureList, f)
		}
		claims.Features = featureList
	}

	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling claims: %v\n", err)
		os.Exit(1)
	}

	sig := ed25519.Sign(ed25519.PrivateKey(privKeyBytes), payloadBytes)

	signed := struct {
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}{
		Payload:   string(payloadBytes),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}

	outBytes, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*output, outBytes, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("License written to %s\n", *output)
	fmt.Printf("  ID:      %s\n", claims.LicenseID)
	fmt.Printf("  Edition: %s\n", claims.Edition)
	fmt.Printf("  Expires: %s\n", time.Unix(claims.ExpiresAt, 0).Format(time.RFC3339))
}

func splitAndTrim(s string) []string {
	var result []string
	for _, part := range splitString(s, ',') {
		trimmed := trimString(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func splitString(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := range len(s) {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func trimString(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
