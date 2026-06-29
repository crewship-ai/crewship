package cli

import (
	"os"
	"path/filepath"
	"strings"
)

// ServerProfile is one named target: a server URL, the CLI token issued for
// that server, and an optional default workspace. Profiles let a single
// ~/.crewship/cli-config.yaml hold credentials for several Crewship instances
// (dev1/dev2/dev3, staging, prod) and switch between them without swapping
// config files. The token is scoped to the profile, so selecting a profile can
// never send another instance's bearer to the wrong host — the same host-
// binding guard that protects --server still applies (see main.serverHost).
type ServerProfile struct {
	Server    string `yaml:"server,omitempty"`
	Token     string `yaml:"token,omitempty"`
	Workspace string `yaml:"workspace,omitempty"`
}

// ActiveProfileName resolves which server profile is active, in precedence
// order:
//
//	--profile flag > CREWSHIP_PROFILE env > directory match > cfg.Current
//
// It returns "" when no profile is selected (single-server / legacy mode), in
// which case the top-level Server/Token/Workspace fields are used directly.
// An empty CREWSHIP_PROFILE is treated as unset.
func ActiveProfileName(flagProfile string, cfg *CLIConfig) string {
	if flagProfile != "" {
		return flagProfile
	}
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_PROFILE")); v != "" {
		return v
	}
	if cfg == nil {
		return ""
	}
	if len(cfg.DirectoryProfiles) > 0 {
		if cwd, err := os.Getwd(); err == nil {
			if name := matchDirectoryProfile(cwd, cfg.DirectoryProfiles); name != "" {
				return name
			}
		}
	}
	return cfg.Current
}

// matchDirectoryProfile returns the profile mapped to the longest configured
// directory that contains cwd (the directory itself or an ancestor). Matching
// is on path boundaries, so "/w/crewship_1" never matches a cwd under
// "/w/crewship_10". Returns "" when nothing matches.
func matchDirectoryProfile(cwd string, dirs map[string]string) string {
	cwd = filepath.Clean(cwd)
	best, bestLen := "", -1
	for dir, name := range dirs {
		d := filepath.Clean(dir)
		if cwd == d || strings.HasPrefix(cwd, d+string(os.PathSeparator)) {
			if len(d) > bestLen {
				best, bestLen = name, len(d)
			}
		}
	}
	return best
}

// ActiveProfile returns the active profile name and its definition. The
// *ServerProfile is nil when no profile is selected, or when the selected name
// has no entry in Servers (e.g. a stale CREWSHIP_PROFILE or `current` pointing
// at a removed profile) — callers can distinguish "no profile" (name == "")
// from "selected but undefined" (name != "", profile == nil).
func (cfg *CLIConfig) ActiveProfile(flagProfile string) (string, *ServerProfile) {
	name := ActiveProfileName(flagProfile, cfg)
	if name == "" || cfg == nil || cfg.Servers == nil {
		return name, nil
	}
	return name, cfg.Servers[name]
}

// WithActiveProfile returns a shallow copy of cfg whose Server/Token/Workspace
// are taken from the active profile when one is active and defined. The profile
// is authoritative: even an empty field overrides the top-level value, so
// selecting a tokenless profile clears the token rather than leaking the
// previous target's bearer to a new host. Global preferences (Format,
// DefaultAgent, Markdown, …) and the Servers/DirectoryProfiles maps are carried
// through unchanged so the result still round-trips through SaveConfig. The
// receiver is never mutated; a nil receiver returns nil.
func (cfg *CLIConfig) WithActiveProfile(flagProfile string) *CLIConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg // copy struct; map headers are shared (treated read-only here)
	if _, p := cfg.ActiveProfile(flagProfile); p != nil {
		out.Server = p.Server
		out.Token = p.Token
		out.Workspace = p.Workspace
	}
	return &out
}
