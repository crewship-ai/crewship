package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/cli"
)

// readValueStdin preserves opaque and multiline values. For token types only,
// accept one terminal line ending added by a shell pipe.
func readValueStdin(credentialType string) (string, error) {
	value, err := readCredentialValueLimit(os.Stdin, "stdin", 64*1024+2)
	if err != nil {
		return "", err
	}

	return credentialStdinValue(credentialType, value)
}

func credentialStdinValue(credentialType, value string) (string, error) {
	switch credentialType {
	case "API_KEY", "CLI_TOKEN", "AI_CLI_TOKEN", "OAUTH2", "ENDPOINT_URL", "PROVIDER_LOGIN":
		if strings.HasSuffix(value, "\n") {
			value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
		}
	}
	if len(value) > 64*1024 {
		return "", fmt.Errorf("stdin value is too long (max 65536 bytes)")
	}
	return value, nil
}

// Existing credentials take their import semantics from server metadata, never
// from the secret's shape. The GET returns no secret value.
func readExistingCredentialStdin(client *cli.Client, id string, expectedType ...string) (string, error) {
	resp, err := client.Get("/api/v1/credentials/" + id)
	if err != nil {
		return "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var meta struct {
		Type string `json:"type" yaml:"type"`
	}
	if err := cli.ReadJSON(resp, &meta); err != nil {
		return "", err
	}
	if len(expectedType) > 0 && meta.Type != expectedType[0] {
		return "", fmt.Errorf("--password-stdin requires an existing USERPASS credential")
	}
	return readValueStdin(meta.Type)
}

// readCredentialValue applies the same bounded import contract to files and stdin.
func readCredentialValue(input io.Reader, source string) (string, error) {
	return readCredentialValueLimit(input, source, 64*1024)
}

func readCredentialValueLimit(input io.Reader, source string, maxValueStdinBytes int64) (string, error) {
	b, err := io.ReadAll(io.LimitReader(input, maxValueStdinBytes+1))
	if err != nil {
		return "", fmt.Errorf("read value from %s: %w", source, err)
	}
	if int64(len(b)) > maxValueStdinBytes {
		return "", fmt.Errorf("%s value is too long (max %d bytes)", source, maxValueStdinBytes)
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("%s contains NUL bytes; binary credential import is not supported by the text/JSON API", source)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("%s contains non-UTF-8 bytes; binary credential import is not supported by the text/JSON API", source)
	}
	return string(b), nil
}
