package cli

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// workingDir is the directory used for directory_profiles matching. The CLI
// layer injects it once at startup via SetWorkingDir, so internal profile
// resolution never reaches into the filesystem (provider pattern) and stays
// deterministic in tests.
var workingDir string

// SetWorkingDir records the process working directory for directory-based
// profile selection. cmd/crewship calls this from PersistentPreRun; tests set
// it explicitly. Empty (the default) disables directory matching.
func SetWorkingDir(dir string) { workingDir = dir }

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

// ProfileSource identifies which precedence layer resolved the active
// profile name — see ActiveProfileNameWithSource. Introduced for #1210:
// the directory_profiles cwd-match layer outranks the persisted `server
// use` default but gave no indication anywhere in the CLI's output that
// it had won, making `server use` look silently broken from inside a
// directory-mapped clone. Surfacing the source lets `server current` /
// `server list` explain themselves instead of just showing a name.
type ProfileSource string

const (
	// ProfileSourceFlag means --profile supplied the name.
	ProfileSourceFlag ProfileSource = "flag"
	// ProfileSourceEnv means CREWSHIP_PROFILE supplied the name.
	ProfileSourceEnv ProfileSource = "env"
	// ProfileSourceDirectory means a directory_profiles cwd match supplied
	// the name — the undocumented 4th precedence layer from #1210.
	ProfileSourceDirectory ProfileSource = "directory"
	// ProfileSourcePersisted means cfg.Current (the `server use`-persisted
	// default) supplied the name.
	ProfileSourcePersisted ProfileSource = "persisted"
	// ProfileSourceNone means no profile is active (legacy single-server
	// mode; top-level Server/Token/Workspace fields are used directly).
	ProfileSourceNone ProfileSource = "none"
)

// ActiveProfileName resolves which server profile is active, in precedence
// order:
//
//	--profile flag > CREWSHIP_PROFILE env > directory match > cfg.Current
//
// It returns "" when no profile is selected (single-server / legacy mode), in
// which case the top-level Server/Token/Workspace fields are used directly.
// An empty CREWSHIP_PROFILE is treated as unset.
//
// This is a thin wrapper around ActiveProfileNameWithSource for callers that
// only need the name.
func ActiveProfileName(flagProfile string, cfg *CLIConfig) string {
	name, _ := ActiveProfileNameWithSource(flagProfile, cfg)
	return name
}

// ActiveProfileNameWithSource resolves the active profile name exactly like
// ActiveProfileName, and additionally reports which precedence layer
// produced it (see ProfileSource). Callers that only need the name should
// keep using ActiveProfileName; this variant exists for `server current` /
// `server list` to explain *why* a given profile is active (#1210).
func ActiveProfileNameWithSource(flagProfile string, cfg *CLIConfig) (string, ProfileSource) {
	if flagProfile != "" {
		return flagProfile, ProfileSourceFlag
	}
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_PROFILE")); v != "" {
		return v, ProfileSourceEnv
	}
	if cfg == nil {
		return "", ProfileSourceNone
	}
	if len(cfg.DirectoryProfiles) > 0 && workingDir != "" {
		if name := matchDirectoryProfile(workingDir, cfg.DirectoryProfiles); name != "" {
			return name, ProfileSourceDirectory
		}
	}
	if cfg.Current != "" {
		return cfg.Current, ProfileSourcePersisted
	}
	return "", ProfileSourceNone
}

// matchDirectoryProfile returns the profile mapped to the longest configured
// directory that contains cwd (the directory itself or an ancestor). Matching
// is on path boundaries, so "/w/crewship_1" never matches a cwd under
// "/w/crewship_10". Returns "" when nothing matches.
func matchDirectoryProfile(cwd string, dirs map[string]string) string {
	name, _ := matchDirectoryProfileDir(cwd, dirs)
	return name
}

// MatchDirectoryProfileDir is the exported counterpart of
// matchDirectoryProfileDir, matching against the working directory recorded
// via SetWorkingDir. Callers (e.g. `server current`) use this to show which
// configured directory triggered a ProfileSourceDirectory resolution.
// Returns ("", "") when directory matching is disabled (no working
// directory recorded) or nothing matches.
func MatchDirectoryProfileDir(dirs map[string]string) (name string, dir string) {
	if workingDir == "" {
		return "", ""
	}
	return matchDirectoryProfileDir(workingDir, dirs)
}

// matchDirectoryProfileDir is matchDirectoryProfile plus the matched
// directory key itself (cleaned), so callers can explain *which* configured
// directory triggered the override rather than just the resulting profile
// name. Returns ("", "") when nothing matches.
func matchDirectoryProfileDir(cwd string, dirs map[string]string) (name string, dir string) {
	cwd = filepath.Clean(cwd)
	bestLen := -1
	for d, n := range dirs {
		cd := filepath.Clean(d)
		if cwd == cd || strings.HasPrefix(cwd, cd+string(os.PathSeparator)) {
			if len(cd) > bestLen {
				name, dir, bestLen = n, cd, len(cd)
			}
		}
	}
	return name, dir
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
		p := cfg.EnsureServer(name)
		if p.Server != "" && !sameHost(p.Server, server) {
			p.Token = "" // old bearer was issued for a different host
		}
		p.Server = server
		return
	}
	if cfg.Server != "" && !sameHost(cfg.Server, server) {
		cfg.Token = ""
	}
	cfg.Server = server
}

// sameHost reports whether two server URLs share a hostname (case-insensitive).
// Used to decide whether repointing a server invalidates the stored token,
// which is bound to its issuing host (see main.serverHost / token-host binding).
func sameHost(a, b string) bool {
	return profileHost(a) == profileHost(b)
}

func profileHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(raw)
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
//
// It delegates to EffectiveServerWithSource so the precedence chain exists in
// exactly one place: `crewship doctor` reports which layer won, and an
// attribution that could drift from the URL actually dialed would be worse
// than no attribution at all.
func EffectiveServer(flagServer, flagProfile string, cfg *CLIConfig) string {
	server, _ := EffectiveServerWithSource(flagServer, flagProfile, cfg)
	return server
}

// ServerSource identifies which configuration layer supplied the effective
// server URL. It is the server-URL counterpart of ProfileSource: knowing
// *that* the CLI dials http://localhost:8080 is far less useful than knowing
// *why* — a stale CREWSHIP_SERVER in one shell and a `server:` line in
// cli-config.yaml look identical in every command's output, which is exactly
// the confusion `crewship doctor` exists to remove.
type ServerSource string

const (
	// ServerSourceFlag means the per-command --server flag supplied the URL.
	ServerSourceFlag ServerSource = "flag"
	// ServerSourceProfile means the active profile supplied it (or was
	// selected but carries no server, in which case the URL is empty — the
	// documented fail-closed behaviour of EffectiveServer).
	ServerSourceProfile ServerSource = "profile"
	// ServerSourceEnv means the CREWSHIP_SERVER environment variable did.
	ServerSourceEnv ServerSource = "env"
	// ServerSourceConfig means the top-level `server:` field in
	// cli-config.yaml did.
	ServerSourceConfig ServerSource = "config"
	// ServerSourceDefault means nothing was configured and the built-in
	// http://localhost:8080 fallback applies.
	ServerSourceDefault ServerSource = "default"
)

// EffectiveServerWithSource resolves the server URL exactly like
// EffectiveServer and additionally reports which precedence layer produced
// it. Callers that only need the URL should keep using EffectiveServer; this
// variant exists so `crewship doctor` can attribute the host it probed
// instead of leaving the operator to guess between flag, profile, env and
// config file.
//
// The two functions are kept in lockstep by construction: every branch below
// mirrors one branch of EffectiveServer (and ResolveServer's env > config >
// default tail), and cmd/crewship's doctor test asserts they agree on the URL
// for every layer.
func EffectiveServerWithSource(flagServer, flagProfile string, cfg *CLIConfig) (string, ServerSource) {
	if flagServer != "" {
		return flagServer, ServerSourceFlag
	}
	if name, p := cfg.ActiveProfile(flagProfile); name != "" {
		if p != nil && p.Server != "" {
			return p.Server, ServerSourceProfile
		}
		// Selected but serverless: fail closed, and still attribute it to the
		// profile — "no server" under a named profile is a profile problem,
		// not a missing env var.
		return "", ServerSourceProfile
	}
	if v := os.Getenv("CREWSHIP_SERVER"); v != "" {
		return v, ServerSourceEnv
	}
	if cfg != nil && cfg.Server != "" {
		return cfg.Server, ServerSourceConfig
	}
	return ResolveServer("", cfg), ServerSourceDefault
}
