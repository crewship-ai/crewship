package main

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// seedPreflight runs before bootstrap, nuke, or any other server mutation.
// Seed deliberately does not load cwd dotenv files or infer a target from cwd.
func seedPreflight(cmd *cobra.Command) error {
	server := strings.TrimSpace(flagServer)
	if server == "" {
		return fmt.Errorf("seed requires an explicit --server URL; profiles, CREWSHIP_SERVER and the working directory cannot select a seed target")
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("--server must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	flagServer = strings.TrimRight(server, "/")
	offline, _ := cmd.Flags().GetBool("offline-demo")
	if offline {
		for _, name := range []string{"smoke-test", "test-backup"} {
			if value, _ := cmd.Flags().GetBool(name); value {
				return fmt.Errorf("--offline-demo cannot be combined with --%s", name)
			}
		}
	}
	applySeedCodexAuthFlag(cmd)
	if offline && seedCodexEnabled() {
		return fmt.Errorf("--offline-demo cannot be combined with a Codex login")
	}
	login, err := resolveSeedCodexLogin()
	if err != nil {
		return err
	}
	if !offline && login == nil && strings.TrimSpace(os.Getenv("SEED_ANTHROPIC_API_KEY")) == "" {
		return fmt.Errorf("seed requires SEED_ANTHROPIC_API_KEY (API key or Claude OAuth token), or a renewable Codex login via --codex-auth-file / SEED_CODEX_AUTH_FILE; no data was created")
	}
	// Only credentials belonging to the explicit URL may supply the seed scope.
	// In particular, a directory-selected profile must never supply its workspace
	// to a destructive seed of another installation on the same host.
	raw, err := cli.LoadConfig()
	if err != nil {
		return fmt.Errorf("load seed target credentials: %w", err)
	}
	cliCfg = raw // discard the cwd-selected overlay
	out := *raw
	out.Current, out.DirectoryProfiles = "", nil
	name := flagProfile
	if name == "" {
		name = strings.TrimSpace(os.Getenv("CREWSHIP_PROFILE"))
	}
	if p := raw.Servers[name]; name != "" && p != nil && sameSeedServer(p.Server, flagServer) {
		out.Server, out.Token, out.Workspace = p.Server, p.Token, p.Workspace
		cliCfg = &out
		return nil
	}
	if flagProfile != "" {
		return fmt.Errorf("--profile %q does not match the explicit --server target", flagProfile)
	}
	if sameSeedServer(out.Server, flagServer) {
		cliCfg = &out
		return nil
	}
	out.Server, out.Token, out.Workspace = flagServer, "", ""
	names := make([]string, 0, len(out.Servers))
	for name := range out.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := out.Servers[name]
		if p != nil && sameSeedServer(p.Server, flagServer) {
			out.Server, out.Token, out.Workspace = p.Server, p.Token, p.Workspace
			break
		}
	}
	cliCfg = &out
	return nil
}

func sameSeedServer(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

// Save into an existing URL-matching profile, or a deterministic new one.
// Select the initial profile only for a completely virgin client config.
// Never repoint existing defaults, directory mappings, or legacy credentials.
func saveSeedCredential(cfg *cli.CLIConfig, server, token, workspace string) string {
	virgin := cfg.Current == "" && cfg.Server == "" && cfg.Token == "" && cfg.Workspace == "" && len(cfg.DirectoryProfiles) == 0 && len(cfg.Servers) == 0
	preferred := flagProfile
	if preferred == "" {
		preferred = strings.TrimSpace(os.Getenv("CREWSHIP_PROFILE"))
	}
	if p := cfg.Servers[preferred]; preferred != "" && p != nil && sameSeedServer(p.Server, server) {
		p.Token, p.Workspace = token, workspace
		return preferred
	}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Servers[name]
		if p != nil && sameSeedServer(p.Server, server) {
			p.Token, p.Workspace = token, workspace
			return name
		}
	}
	sum := sha256.Sum256([]byte(strings.TrimRight(server, "/")))
	name := fmt.Sprintf("seed-%x", sum[:8])
	// An operator-created profile with the same generated name is never replaced.
	for suffix := 1; cfg.Servers[name] != nil && !sameSeedServer(cfg.Servers[name].Server, server); suffix++ {
		name = fmt.Sprintf("seed-%x-%d", sum[:8], suffix)
	}
	p := cfg.EnsureServer(name)
	p.Server, p.Token, p.Workspace = server, token, workspace
	if virgin {
		cfg.Current = name
	}
	return name
}
