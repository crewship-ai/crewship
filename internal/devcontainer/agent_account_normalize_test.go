package devcontainer

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// common-utils reads userUid / userGid. Older versions injected uid / gid,
// which it ignores, so the account got an automatic UID: 1000 on a slim
// image, where the runtime's UID 1001 then had no account.
func TestNormalizeAgentUser(t *testing.T) {
	cu := commonUtilsFeatureRef
	cases := []struct {
		name        string
		features    map[string]map[string]any
		want        map[string]any
		wantChanged bool
		wantErr     bool
	}{
		{
			name:        "absent is injected with the keys common-utils reads",
			features:    nil,
			want:        map[string]any{"username": "agent", "userUid": "1001", "userGid": "1001", "installZsh": "false", "upgradePackages": "false", "configureZshAsDefaultShell": "false"},
			wantChanged: true,
		},
		{
			name:        "legacy injected uid/gid keys are migrated",
			features:    map[string]map[string]any{cu: {"username": "agent", "uid": "1001", "gid": "1001", "installZsh": "false"}},
			want:        map[string]any{"username": "agent", "userUid": "1001", "userGid": "1001", "installZsh": "false"},
			wantChanged: true,
		},
		{
			name:        "declared without account options is filled, other options kept",
			features:    map[string]map[string]any{"ghcr.io/devcontainers/features/common-utils:1": {"installZsh": "true"}},
			want:        map[string]any{"username": "agent", "userUid": "1001", "userGid": "1001", "installZsh": "true"},
			wantChanged: true,
		},
		{
			name:        "automatic is replaced",
			features:    map[string]map[string]any{cu: {"username": "automatic", "userUid": "automatic", "userGid": "automatic"}},
			want:        map[string]any{"username": "agent", "userUid": "1001", "userGid": "1001"},
			wantChanged: true,
		},
		{
			name:     "already explicit is unchanged (cache key stays)",
			features: map[string]map[string]any{cu: {"username": "agent", "userUid": "1001", "userGid": "1001"}},
			want:     map[string]any{"username": "agent", "userUid": "1001", "userGid": "1001"},
		},
		{
			name:        "numeric 1001 is accepted and written as a string",
			features:    map[string]map[string]any{cu: {"username": "agent", "userUid": float64(1001), "userGid": "1001"}},
			want:        map[string]any{"username": "agent", "userUid": "1001", "userGid": "1001"},
			wantChanged: true,
		},
		{name: "another username is refused", features: map[string]map[string]any{cu: {"username": "vscode"}}, wantErr: true},
		{name: "another UID is refused", features: map[string]map[string]any{cu: {"userUid": "1000"}}, wantErr: true},
		{name: "legacy uid other than 1001 is refused", features: map[string]map[string]any{cu: {"uid": "2000"}}, wantErr: true},
		{name: "legacy and current keys disagree", features: map[string]map[string]any{cu: {"uid": "1001", "userUid": "1000"}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Image: "debian:bookworm-slim", Features: tc.features}
			changed, err := c.NormalizeAgentUser()
			if tc.wantErr {
				if !errors.Is(err, ErrAgentAccountConflict) {
					t.Fatalf("err = %v, want ErrAgentAccountConflict", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tc.wantChanged)
			}
			var got map[string]any
			for ref, opts := range c.Features {
				if isCommonUtilsRef(ref) {
					got = opts
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("options = %v, want %v", got, tc.want)
			}
			// Idempotent: a second pass changes nothing.
			if again, err := c.NormalizeAgentUser(); err != nil || again {
				t.Fatalf("second pass changed=%v err=%v", again, err)
			}
		})
	}
}

// The legacy options and the normalized ones must hash differently, so a
// crew built with the ignored keys rebuilds once; an explicit config keeps
// its hash and its cached image.
func TestNormalizeAgentUser_CacheKey(t *testing.T) {
	legacy := &Config{Image: "debian:bookworm-slim", Features: map[string]map[string]any{commonUtilsFeatureRef: {"username": "agent", "uid": "1001", "gid": "1001"}}}
	before := configHash("debian:bookworm-slim", legacy, "", "fp")
	if _, err := legacy.NormalizeAgentUser(); err != nil {
		t.Fatal(err)
	}
	after := configHash("debian:bookworm-slim", legacy, "", "fp")
	if before == after {
		t.Fatal("legacy config kept its cache key; its image would not be rebuilt with the account")
	}
	if _, err := legacy.NormalizeAgentUser(); err != nil {
		t.Fatal(err)
	}
	if again := configHash("debian:bookworm-slim", legacy, "", "fp"); again != after {
		t.Fatal("normalized config's cache key is not stable")
	}
}

// What install.sh actually receives: with the legacy keys the metadata
// default USERUID=automatic won; after normalization it is 1001.
func TestNormalizeAgentUser_BuildEnvCarriesUID(t *testing.T) {
	meta := FeatureMetadata{Options: map[string]any{
		"username": map[string]any{"default": "automatic"},
		"userUid":  map[string]any{"default": "automatic"},
		"userGid":  map[string]any{"default": "automatic"},
	}}
	legacy := map[string]any{"username": "agent", "uid": "1001", "gid": "1001"}
	if env := featureBuildEnv(meta, legacy); !containsEnv(env, "USERUID", "automatic") {
		t.Fatalf("precondition: legacy keys should leave USERUID automatic, got %v", env)
	}
	c := &Config{Image: "debian:bookworm-slim", Features: map[string]map[string]any{commonUtilsFeatureRef: legacy}}
	if _, err := c.NormalizeAgentUser(); err != nil {
		t.Fatal(err)
	}
	env := featureBuildEnv(meta, c.Features[commonUtilsFeatureRef])
	if !containsEnv(env, "USERUID", "1001") || !containsEnv(env, "USERGID", "1001") || !containsEnv(env, "USERNAME", "agent") {
		t.Fatalf("build env = %v, want USERNAME=agent USERUID=1001 USERGID=1001", env)
	}
}

func containsEnv(env []string, key, val string) bool {
	for _, e := range env {
		if e == key+"="+val || e == key+"='"+val+"'" {
			return true
		}
	}
	return false
}

// Provisioning normalizes only a declared common-utils: a config without one
// keeps its shape, so a config with nothing to build still skips the build.
func TestNormalizeDeclaredAgentUser_DoesNotInject(t *testing.T) {
	c := &Config{Image: "debian:bookworm-slim"}
	changed, err := c.NormalizeDeclaredAgentUser()
	if err != nil || changed || len(c.Features) != 0 {
		t.Fatalf("changed=%v err=%v features=%v, want untouched", changed, err, c.Features)
	}
}

func TestNormalizeAgentUser_ReviewCases(t *testing.T) {
	cu := commonUtilsFeatureRef
	for _, tc := range []struct {
		name string
		opts map[string]any
		want map[string]any
	}{
		{"none creates no account: left alone", map[string]any{"username": "none", "installZsh": "false"}, map[string]any{"username": "none", "installZsh": "false"}},
		{"root creates no account: left alone", map[string]any{"username": "root"}, map[string]any{"username": "root"}},
		{"legacy value beats automatic", map[string]any{"uid": "1001", "userUid": "automatic"}, map[string]any{"username": "agent", "userUid": "1001", "userGid": "1001"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Image: "x", Features: map[string]map[string]any{cu: tc.opts}}
			if _, err := c.NormalizeAgentUser(); err != nil {
				t.Fatal(err)
			}
			if got := c.Features[cu]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("options = %v, want %v", got, tc.want)
			}
		})
	}
}

// Provisioning refuses a conflicting declaration before any image work.
func TestProvision_RefusesConflictingAgentAccount(t *testing.T) {
	p := NewProvisioner(nil, nil, nil, nil)
	cfg := &Config{Image: "debian:bookworm-slim", Features: map[string]map[string]any{commonUtilsFeatureRef: {"username": "vscode"}}}
	if _, err := p.Provision(context.Background(), cfg.Image, cfg, ""); !errors.Is(err, ErrAgentAccountConflict) {
		t.Fatalf("err = %v, want ErrAgentAccountConflict", err)
	}
}
