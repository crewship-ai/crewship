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
// are taken from the active profile when one is active. The profile is
// authoritative: even an empty field overrides the top-level value, so
// selecting a tokenless profile clears the token rather than leaking the
// previous target's bearer to a new host. When a profile is *selected but
// undefined* (a typo'd --profile / CREWSHIP_PROFILE, or `current` pointing at a
// removed profile), the target fields are blanked so reads fail closed against
// the legacy/default target instead of silently using the wrong creds. Global
// preferences (Format, DefaultAgent, Markdown, …) and the Servers/
// DirectoryProfiles maps are carried through unchanged so the result still
// round-trips through SaveConfig. The receiver is never mutated; a nil receiver
// returns nil.
func (cfg *CLIConfig) WithActiveProfile(flagProfile string) *CLIConfig {
	if cfg == nil {
		return nil
	}
	out := *cfg // copy struct; map headers are shared (treated read-only here)
	if name, p := cfg.ActiveProfile(flagProfile); name != "" {
		if p != nil {
			out.Server, out.Token, out.Workspace = p.Server, p.Token, p.Workspace
		} else {
			out.Server, out.Token, out.Workspace = "", "", ""
		}
	}
	return &out
}

// EnsureServer returns the profile entry for name, creating it (and the Servers
// map) if absent. Centralizes the get-or-create upsert used by the write
// helpers and `crewship server add`.
func (cfg *CLIConfig) EnsureServer(name string) *ServerProfile {
	if cfg.Servers == nil {
		cfg.Servers = map[string]*ServerProfile{}
	}
	p := cfg.Servers[name]
	if p == nil {
		p = &ServerProfile{}
		cfg.Servers[name] = p
	}
	return p
}

// WriteCredential stores a server URL + token at the active write target: the
// active profile (resolved via flagProfile / CREWSHIP_PROFILE / directory match
// / current, created if needed) when one is selected, else the legacy top-level
// fields. A non-empty workspace is written too; an empty one is left untouched
// so a token refresh doesn't wipe a previously chosen workspace. The first
// profile written becomes `current`, but an existing `current` is never
// re-pointed (so a one-off `login --profile X` refresh doesn't move the default
// target). Writes land where WithActiveProfile reads, keeping the two sides
// symmetric.
func (cfg *CLIConfig) WriteCredential(flagProfile, server, token, workspace string) {
	name := ActiveProfileName(flagProfile, cfg)
	if name == "" {
		cfg.Server = server
		cfg.Token = token
		if workspace != "" {
			cfg.Workspace = workspace
		}
		return
	}
	p := cfg.EnsureServer(name)
	p.Server = server
	p.Token = token
	if workspace != "" {
		p.Workspace = workspace
	}
	if cfg.Current == "" {
		cfg.Current = name
	}
}

// SetServerTarget / SetWorkspaceTarget set a single field on the active write
// target (the active profile, created if needed, else the legacy top-level
// field) so `config set` and `workspace use` land where reads look.
func (cfg *CLIConfig) SetServerTarget(flagProfile, server string) {
	if name := ActiveProfileName(flagProfile, cfg); name != "" {
		cfg.EnsureServer(name).Server = server
		return
	}
	cfg.Server = server
}

func (cfg *CLIConfig) SetWorkspaceTarget(flagProfile, workspace string) {
	if name := ActiveProfileName(flagProfile, cfg); name != "" {
		cfg.EnsureServer(name).Workspace = workspace
		return
	}
	cfg.Workspace = workspace
}

// ClearTokenTarget clears the token on the active write target, and always
// clears any top-level token too, so `crewship logout` never leaves a live
// bearer on disk (in profile mode the top-level token is unused, so wiping it
// is safe; sibling profiles keep their tokens).
func (cfg *CLIConfig) ClearTokenTarget(flagProfile string) {
	if name := ActiveProfileName(flagProfile, cfg); name != "" {
		if p := cfg.Servers[name]; p != nil {
			p.Token = ""
		}
	}
	cfg.Token = ""
}

// EffectiveServer resolves the server URL the CLI should dial, honoring an
// active profile over a stale CREWSHIP_SERVER env (the #544 stopgap that
// profiles replace). Precedence: --server flag > active profile's server >
// CREWSHIP_SERVER env > top-level server > default. In legacy mode (no profile
// active) it is identical to ResolveServer.
func EffectiveServer(flagServer, flagProfile string, cfg *CLIConfig) string {
	if flagServer != "" {
		return flagServer
	}
	if name, p := cfg.ActiveProfile(flagProfile); name != "" && p != nil && p.Server != "" {
		return p.Server
	}
	return ResolveServer("", cfg)
}
