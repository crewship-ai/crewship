package cli

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestActiveProfileName(t *testing.T) {
	cfg := &CLIConfig{
		Current: "cur",
		Servers: map[string]*ServerProfile{
			"cur":  {Server: "https://cur"},
			"dev2": {Server: "https://dev2"},
		},
	}
	tests := []struct {
		name string
		flag string
		env  string
		cfg  *CLIConfig
		want string
	}{
		{"flag beats env and current", "dev2", "envp", cfg, "dev2"},
		{"env beats current", "", "dev2", cfg, "dev2"},
		{"current fallback", "", "", cfg, "cur"},
		{"none selected", "", "", &CLIConfig{}, ""},
		{"nil cfg", "", "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv with "" still SETS the var to empty; ActiveProfileName
			// must treat an empty CREWSHIP_PROFILE as unset.
			t.Setenv("CREWSHIP_PROFILE", tt.env)
			got := ActiveProfileName(tt.flag, tt.cfg)
			if got != tt.want {
				t.Errorf("ActiveProfileName(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
}

func TestMatchDirectoryProfile(t *testing.T) {
	m := map[string]string{
		"/work/crewship_1": "dev1",
		"/work/crewship_2": "dev2",
		"/work":            "base",
	}
	tests := []struct {
		cwd  string
		want string
	}{
		{"/work/crewship_1", "dev1"},              // exact
		{"/work/crewship_1/internal/cli", "dev1"}, // descendant
		{"/work/crewship_2", "dev2"},
		{"/work/other", "base"},       // only the shorter /work matches
		{"/work/crewship_10", "base"}, // must NOT loose-prefix-match crewship_1
		{"/elsewhere", ""},            // no match
		{"/work", "base"},             // exact on the shortest key
	}
	for _, tt := range tests {
		t.Run(tt.cwd, func(t *testing.T) {
			if got := matchDirectoryProfile(tt.cwd, m); got != tt.want {
				t.Errorf("matchDirectoryProfile(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestMatchDirectoryProfileEmpty(t *testing.T) {
	if got := matchDirectoryProfile("/anywhere", nil); got != "" {
		t.Errorf("nil map: got %q, want empty", got)
	}
}

func TestActiveProfileLookup(t *testing.T) {
	t.Setenv("CREWSHIP_PROFILE", "")
	cfg := &CLIConfig{
		Current: "dev2",
		Servers: map[string]*ServerProfile{
			"dev2": {Server: "https://dev2", Token: "t2"},
		},
	}
	name, p := cfg.ActiveProfile("")
	if name != "dev2" || p == nil || p.Server != "https://dev2" {
		t.Fatalf("ActiveProfile() = %q,%+v", name, p)
	}

	// Selected name with no entry → nil profile but name preserved.
	stale := &CLIConfig{Current: "ghost"}
	name, p = stale.ActiveProfile("")
	if name != "ghost" || p != nil {
		t.Fatalf("stale profile: name=%q p=%+v, want ghost,nil", name, p)
	}
}

func TestWithActiveProfile(t *testing.T) {
	t.Setenv("CREWSHIP_PROFILE", "")
	base := &CLIConfig{
		Server: "https://top", Token: "toptok", Workspace: "topws", Format: "table",
		Current: "dev2",
		Servers: map[string]*ServerProfile{
			"dev2": {Server: "https://dev2", Token: "dev2tok", Workspace: "dev2ws"},
		},
	}

	got := base.WithActiveProfile("")
	if got.Server != "https://dev2" || got.Token != "dev2tok" || got.Workspace != "dev2ws" {
		t.Errorf("overlay not applied: server=%q token=%q ws=%q", got.Server, got.Token, got.Workspace)
	}
	if got.Format != "table" {
		t.Errorf("global pref Format lost in overlay: %q", got.Format)
	}
	if got.Servers["dev2"] == nil {
		t.Errorf("Servers map dropped from overlay copy (would break SaveConfig round-trip)")
	}
	// Receiver must be untouched so a later SaveConfig writes the real config.
	if base.Server != "https://top" || base.Token != "toptok" {
		t.Errorf("WithActiveProfile mutated receiver: %+v", base)
	}

	// No active profile → top-level retained verbatim.
	legacy := &CLIConfig{Server: "https://top", Token: "t"}
	g2 := legacy.WithActiveProfile("")
	if g2.Server != "https://top" || g2.Token != "t" {
		t.Errorf("no-profile overlay altered config: %+v", g2)
	}

	// Active profile authoritative even when a field is empty: selecting a
	// tokenless profile must NOT leak the top-level token to the new host.
	notoken := &CLIConfig{
		Server: "https://top", Token: "toptok",
		Current: "fresh",
		Servers: map[string]*ServerProfile{"fresh": {Server: "https://fresh"}},
	}
	g3 := notoken.WithActiveProfile("")
	if g3.Server != "https://fresh" || g3.Token != "" {
		t.Errorf("tokenless profile leaked creds: server=%q token=%q", g3.Server, g3.Token)
	}

	// nil receiver is safe.
	var nilcfg *CLIConfig
	if nilcfg.WithActiveProfile("") != nil {
		t.Errorf("nil receiver should return nil")
	}
}

func TestServersRoundTrip(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "c.yaml")
	orig := &CLIConfig{
		Format:  "json",
		Current: "dev2",
		Servers: map[string]*ServerProfile{
			"dev1": {Server: "https://dev1", Token: "t1", Workspace: "w1"},
			"dev2": {Server: "https://dev2", Token: "t2"},
		},
		DirectoryProfiles: map[string]string{"/work/c1": "dev1"},
	}
	data, err := yaml.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := loadConfigFrom(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Current != "dev2" {
		t.Errorf("Current = %q", got.Current)
	}
	if got.Servers["dev1"].Token != "t1" || got.Servers["dev1"].Workspace != "w1" {
		t.Errorf("dev1 profile lost: %+v", got.Servers["dev1"])
	}
	if got.Servers["dev2"].Server != "https://dev2" {
		t.Errorf("dev2 profile lost: %+v", got.Servers["dev2"])
	}
	if got.DirectoryProfiles["/work/c1"] != "dev1" {
		t.Errorf("DirectoryProfiles lost: %+v", got.DirectoryProfiles)
	}
}
