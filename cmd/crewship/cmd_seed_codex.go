package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/codexauth"
)

// A path, not the login JSON, belongs in .env.local. The file stays outside
// the repository and is read only by the seed process.
const seedCodexAuthFileEnv = "SEED_CODEX_AUTH_FILE"

func seedCodexEnabled() bool {
	return strings.TrimSpace(os.Getenv(seedCodexAuthFileEnv)) != ""
}

func resolveSeedCodexLogin() (*seeddata.CredentialDef, error) {
	path := strings.TrimSpace(os.Getenv(seedCodexAuthFileEnv))
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s must be an absolute path outside the checkout", seedCodexAuthFileEnv)
	}
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s must be outside the checkout", seedCodexAuthFileEnv)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", seedCodexAuthFileEnv, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s must name a regular auth.json readable only by its owner (mode 0600)", seedCodexAuthFileEnv)
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("%s auth.json exceeds 1 MiB", seedCodexAuthFileEnv)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", seedCodexAuthFileEnv, err)
	}
	login, err := codexauth.Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s is not a Codex ChatGPT login: %w", seedCodexAuthFileEnv, err)
	}
	if login.AuthMode != "chatgpt" || login.OpenAIAPIKey != nil || login.Tokens.RefreshToken == "" {
		return nil, fmt.Errorf("%s must contain a renewable Codex ChatGPT subscription login", seedCodexAuthFileEnv)
	}
	return &seeddata.CredentialDef{
		Name:        "CODEX_DEMO_LOGIN",
		Description: "Codex ChatGPT login for seeded demo agents",
		Type:        "PROVIDER_LOGIN",
		Provider:    "OPENAI",
		Mode:        "subscription",
		EnvVarName:  "OPENAI_API_KEY", // binding identity; subscription login is delivered as a file
		Value:       strings.TrimSpace(string(data)),
	}, nil
}
