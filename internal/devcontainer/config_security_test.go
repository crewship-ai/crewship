package devcontainer

import (
	"encoding/json"
	"errors"
	"testing"
)

// config_security_test.go covers the #1380 top-level container-privilege
// controls: parsing, persistence through canonicalMap (the bug was that these
// keys were parsed-and-discarded), save-time validation, and the filtered
// runtime extraction.

func TestParseBytes_TopLevelSecurityParsed(t *testing.T) {
	cfg, err := ParseBytes([]byte(`{
		"image":"debian:bookworm-slim",
		"privileged":true,
		"init":true,
		"capAdd":["NET_BIND_SERVICE"],
		"mounts":[{"source":"/dev/fuse","target":"/dev/fuse","type":"bind"}]
	}`))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if !cfg.Privileged {
		t.Error("privileged not parsed")
	}
	if !cfg.Init {
		t.Error("init not parsed")
	}
	if len(cfg.CapAdd) != 1 || cfg.CapAdd[0] != "NET_BIND_SERVICE" {
		t.Errorf("capAdd = %v", cfg.CapAdd)
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Source != "/dev/fuse" {
		t.Errorf("mounts = %v", cfg.Mounts)
	}
}

// The pre-#1380 bug: EnsureAgentUser re-marshals the Config, and canonicalMap
// dropped the security keys — so a create that triggered auto-inject silently
// discarded privileged/capAdd/mounts. Guard against regression.
func TestCanonicalMap_PersistsSecurityKeys(t *testing.T) {
	cfg := &Config{
		Image:      "debian:bookworm-slim",
		Privileged: true,
		Init:       true,
		CapAdd:     []string{"NET_BIND_SERVICE"},
		Mounts:     []FeatureMount{{Source: "/dev/fuse", Target: "/dev/fuse", Type: "bind"}},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["privileged"] != true {
		t.Errorf("privileged dropped from canonical form: %s", data)
	}
	if back["init"] != true {
		t.Errorf("init dropped: %s", data)
	}
	if _, ok := back["capAdd"]; !ok {
		t.Errorf("capAdd dropped: %s", data)
	}
	if _, ok := back["mounts"]; !ok {
		t.Errorf("mounts dropped: %s", data)
	}
}

// Runtime-only fields must not perturb the provisioning hash (flipping
// privileged should not force a full image rebuild).
func TestHash_IgnoresSecurityKeys(t *testing.T) {
	base := &Config{Image: "debian:bookworm-slim"}
	priv := &Config{Image: "debian:bookworm-slim", Privileged: true, Init: true,
		CapAdd: []string{"NET_BIND_SERVICE"}, Mounts: []FeatureMount{{Source: "/dev/fuse", Target: "/dev/fuse"}}}
	if base.Hash() != priv.Hash() {
		t.Errorf("hash changed by runtime-only security keys: %s vs %s", base.Hash(), priv.Hash())
	}
}

func TestValidateSecurity(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *Config
		allowPriv bool
		wantErr   error
	}{
		{"privileged without flag", &Config{Image: "x", Privileged: true}, false, ErrPrivilegedNotAllowed},
		{"privileged with flag", &Config{Image: "x", Privileged: true}, true, nil},
		{"allowed cap", &Config{Image: "x", CapAdd: []string{"NET_BIND_SERVICE"}}, false, nil},
		{"allowed cap CAP_ form", &Config{Image: "x", CapAdd: []string{"cap_net_bind_service"}}, false, nil},
		{"disallowed cap", &Config{Image: "x", CapAdd: []string{"SYS_ADMIN"}}, false, ErrCapabilityNotAllowed},
		{"allowed mount", &Config{Image: "x", Mounts: []FeatureMount{{Source: "/dev/fuse", Target: "/dev/fuse"}}}, false, nil},
		{"disallowed mount", &Config{Image: "x", Mounts: []FeatureMount{{Source: "/var/run/docker.sock", Target: "/x"}}}, false, ErrMountNotAllowed},
		{"host path mount", &Config{Image: "x", Mounts: []FeatureMount{{Source: "/etc", Target: "/x"}}}, false, ErrMountNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateSecurity(tc.allowPriv)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestParseConfigSecurity_FiltersUnsafe(t *testing.T) {
	// A tampered stored blob: privileged plus a disallowed cap and mount. The
	// runtime extraction must keep privileged/init but drop the unlisted
	// cap/mount (defense in depth — the save gate already rejects these, but a
	// direct-DB tamper must not smuggle them into the HostConfig).
	req := ParseConfigSecurity(`{
		"image":"x",
		"privileged":true,
		"init":true,
		"capAdd":["NET_BIND_SERVICE","SYS_ADMIN"],
		"mounts":[
			{"source":"/dev/fuse","target":"/dev/fuse"},
			{"source":"/var/run/docker.sock","target":"/var/run/docker.sock"}
		]
	}`)
	if !req.Privileged || !req.Init {
		t.Errorf("privileged/init not honoured: %+v", req)
	}
	if len(req.CapAdd) != 1 || req.CapAdd[0] != "NET_BIND_SERVICE" {
		t.Errorf("capAdd not filtered to allowlist: %v", req.CapAdd)
	}
	if len(req.Mounts) != 1 || req.Mounts[0].Source != "/dev/fuse" {
		t.Errorf("mounts not filtered to allowlist: %v", req.Mounts)
	}
}

func TestParseConfigSecurity_EmptyAndMalformed(t *testing.T) {
	if got := ParseConfigSecurity(""); got.Privileged || len(got.CapAdd) > 0 {
		t.Errorf("empty blob should yield zero value, got %+v", got)
	}
	if got := ParseConfigSecurity(`{not json`); got.Privileged {
		t.Errorf("malformed blob should fail closed (non-privileged), got %+v", got)
	}
}
