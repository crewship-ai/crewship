package devcontainer

import (
	"encoding/json"
	"testing"
)

// TestFeatureMetadata_StripsPrivilegedAndDangerousCaps is the F-011
// regression guard. A devcontainer feature served from an untrusted OCI
// registry can declare privileged: true, capAdd: [SYS_ADMIN, ...] and
// securityOpt: [seccomp=unconfined, apparmor=unconfined] in its
// metadata. Pre-fix those bubbled straight into the container HostConfig
// and undid the --cap-drop=ALL sandbox.
func TestFeatureMetadata_StripsPrivilegedAndDangerousCaps(t *testing.T) {
	raw := []byte(`{
		"id": "evil-feature",
		"version": "1.0.0",
		"name": "Evil",
		"privileged": true,
		"capAdd": ["SYS_ADMIN", "SYS_PTRACE", "NET_ADMIN", "DAC_OVERRIDE"],
		"securityOpt": ["seccomp=unconfined", "apparmor=unconfined", "no-new-privileges:false"]
	}`)
	var meta FeatureMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if meta.Privileged {
		t.Fatalf("Privileged must be false after parse — feature can't grant itself host elevation")
	}
	if len(meta.CapAdd) != 0 {
		t.Fatalf("dangerous CapAdd must be filtered out, got %v", meta.CapAdd)
	}
	if len(meta.SecurityOpt) != 0 {
		t.Fatalf("SecurityOpt overrides must always be dropped, got %v", meta.SecurityOpt)
	}
}

// TestFeatureMetadata_PreservesAllowedCap confirms NET_BIND_SERVICE — the
// one capability we accept from features — survives the filter.
func TestFeatureMetadata_PreservesAllowedCap(t *testing.T) {
	raw := []byte(`{
		"id": "binds-low-port",
		"version": "1.0.0",
		"name": "BindsLowPort",
		"capAdd": ["NET_BIND_SERVICE"]
	}`)
	var meta FeatureMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(meta.CapAdd) != 1 || meta.CapAdd[0] != "NET_BIND_SERVICE" {
		t.Fatalf("NET_BIND_SERVICE must pass the filter, got %v", meta.CapAdd)
	}
}

// TestFeatureMetadata_AllowedCapMixedWithDangerousFiltersDangerous keeps
// only the whitelisted cap when the feature mixes safe and dangerous caps.
func TestFeatureMetadata_AllowedCapMixedWithDangerousFiltersDangerous(t *testing.T) {
	raw := []byte(`{
		"id": "mixed",
		"version": "1.0.0",
		"name": "Mixed",
		"capAdd": ["NET_BIND_SERVICE", "SYS_ADMIN", "NET_RAW"]
	}`)
	var meta FeatureMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(meta.CapAdd) != 1 || meta.CapAdd[0] != "NET_BIND_SERVICE" {
		t.Fatalf("only NET_BIND_SERVICE should survive, got %v", meta.CapAdd)
	}
}

func TestFilterAllowedCapAdd(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{}, nil},
		{[]string{"SYS_ADMIN"}, nil},
		{[]string{"NET_BIND_SERVICE"}, []string{"NET_BIND_SERVICE"}},
		{[]string{"SYS_ADMIN", "NET_BIND_SERVICE", "NET_RAW"}, []string{"NET_BIND_SERVICE"}},
		{[]string{"chown", "NET_BIND_SERVICE"}, []string{"NET_BIND_SERVICE"}}, // case-sensitive on purpose
	}
	for _, tc := range cases {
		got := filterAllowedCapAdd(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("input %v: got %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("input %v: got %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}
