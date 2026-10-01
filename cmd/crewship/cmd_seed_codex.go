package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/codexauth"
)

// A path, not the login JSON, belongs in .env.local. The file stays outside
// the repository and is read only by the seed process.
const seedCodexAuthFileEnv = "SEED_CODEX_AUTH_FILE"

// seedCodexAuthFileOverride carries the --codex-auth-file value once runSeed
// has parsed the flag. It is the one piece of flag state the deep seed phases
// need: seedAgents, seedCredentials and seedRoutines call seedCodexEnabled
// four layers below runSeed, and threading a parameter through them — and the
// dozen tests that invoke them directly — would be the larger change. Empty
// means the flag was not passed and the env var governs.
var seedCodexAuthFileOverride string

// seedCodexAuthFile resolves the effective login path: an explicit
// --codex-auth-file wins over SEED_CODEX_AUTH_FILE (set directly in the shell
// or populated from .env.local by loadDotEnvLocal).
func seedCodexAuthFile() string {
	if path := strings.TrimSpace(seedCodexAuthFileOverride); path != "" {
		return path
	}
	return strings.TrimSpace(os.Getenv(seedCodexAuthFileEnv))
}

// seedCodexAuthFileSource names whichever input supplied the path, so errors
// point at the knob the operator actually turned.
func seedCodexAuthFileSource() string {
	if strings.TrimSpace(seedCodexAuthFileOverride) != "" {
		return "--codex-auth-file"
	}
	return seedCodexAuthFileEnv
}

func seedCodexEnabled() bool {
	return seedCodexAuthFile() != ""
}

func resolveSeedCodexLogin() (*seeddata.CredentialDef, error) {
	path := seedCodexAuthFile()
	source := seedCodexAuthFileSource()
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s must be an absolute path outside the checkout", source)
	}
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s must be outside the checkout", source)
		}
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if !info.Mode().IsRegular() || !authFilePermOK(f, info) {
		return nil, fmt.Errorf("%s must name a regular auth.json readable only by its owner (mode 0600)", source)
	}
	if info.Size() > 1<<20 {
		return nil, fmt.Errorf("%s auth.json exceeds 1 MiB", source)
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	login, err := codexauth.Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s is not a Codex ChatGPT login: %w", source, err)
	}
	if login.AuthMode != "chatgpt" || login.OpenAIAPIKey != nil || login.Tokens.RefreshToken == "" {
		return nil, fmt.Errorf("%s must contain a renewable Codex ChatGPT subscription login", source)
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
