package backup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// The kit must hand the restored server the key string exactly as the
// source had it: the journal chain key is derived from the ENCRYPTION_KEY
// string, so re-encoding upper-case hex as lower-case would open every
// credential and still break chain verification.
func TestRecoveryKitKeepsTheExactKeyString(t *testing.T) {
	upper := strings.ToUpper(strings.Repeat("ab", 32))
	cases := []struct {
		name      string
		env       map[string]string
		scan      map[string]int
		wantLines []string
	}{
		{
			name:      "upper-case v1 survives byte for byte",
			env:       map[string]string{"v1": upper},
			scan:      map[string]int{"v1": 3},
			wantLines: []string{encryption.KeyEnvVar("v1") + "=" + upper},
		},
		{
			name:      "a version without its own variable falls back like Decrypt",
			env:       map[string]string{"v1": upper},
			scan:      map[string]int{"v1": 1, "v3": 2},
			wantLines: []string{encryption.KeyEnvVar("v1") + "=" + upper},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(encryption.KeyVersionEnvVar, "")
			t.Setenv(encryption.KeyEnvVar("v3"), "")
			for v, val := range tc.env {
				t.Setenv(encryption.KeyEnvVar(v), val)
			}
			kit, missing, err := backup.BuildRecoveryKit(backup.VaultScan{Versions: tc.scan}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			// v3 falls back to ENCRYPTION_KEY the way Decrypt does, so it is
			// carried, under its own variable name.
			lines, err := kit.EnvLines()
			if err != nil {
				t.Fatal(err)
			}
			if lines[0] != tc.wantLines[0] {
				t.Fatalf("first line = %q, want %q", lines[0], tc.wantLines[0])
			}
			if kit.ChainSeed() != upper {
				t.Fatalf("chain seed = %q", kit.ChainSeed())
			}
			if len(missing) != 0 {
				t.Fatalf("missing = %v", missing)
			}
		})
	}
}

func TestRecoveryKitMissingKeyIsRecorded(t *testing.T) {
	t.Setenv(encryption.KeyVersionEnvVar, "")
	t.Setenv(encryption.KeyEnvVar("v1"), "")
	_, missing, err := backup.BuildRecoveryKit(backup.VaultScan{Versions: map[string]int{"v1": 4}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if missing["v1"] != 4 {
		t.Fatalf("missing = %v, want v1:4", missing)
	}
}

func TestRecoveryKitAuditRootRetention(t *testing.T) {
	upper := strings.ToUpper(strings.Repeat("ab", 32))
	for _, tc := range []struct {
		name, root string
		wantError  bool
	}{
		{"upper_case_root_without_v1_envelopes", upper, false},
		{"absent_root_is_not_invented", "", false},
		{"malformed_provided_root_refuses_capture", strings.Repeat("z", 64), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(encryption.KeyVersionEnvVar, "v2")
			t.Setenv(encryption.KeyEnvVar("v1"), tc.root)
			t.Setenv(encryption.KeyEnvVar("v2"), strings.Repeat("cd", 32))
			kit, missing, err := backup.BuildRecoveryKit(backup.VaultScan{Versions: map[string]int{"v2": 2}}, time.Now())
			if tc.wantError {
				if err == nil || kit != nil {
					t.Fatal("malformed configured audit root was silently dropped")
				}
				return
			}
			if err != nil || len(missing) != 0 || kit.CurrentVersion != "v2" {
				t.Fatalf("valid configured kit failed: err=%v missing=%d", err, len(missing))
			}
			if kit.ChainSeed() != tc.root {
				t.Fatal("audit root was normalized, lost, or invented")
			}
			lines, err := kit.EnvLines()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, line := range lines {
				if value, ok := strings.CutPrefix(line, "ENCRYPTION_KEY="); ok {
					found = true
					if value != tc.root {
						t.Fatal("recovered root environment changed its exact string")
					}
				}
			}
			if found != (tc.root != "") {
				t.Fatal("recovered root presence does not match explicit configuration")
			}
		})
	}
}

func TestParseRecoveryKitRejectsTampering(t *testing.T) {
	cases := []struct{ name, body string }{
		{"not json", `nope`},
		{"bad version", `{"versions":[{"version":"x1","env":"ENCRYPTION_KEY","key_b64":"AA=="}]}`},
		{"env mismatch", `{"versions":[{"version":"v1","env":"PATH","key_b64":"` + strings.Repeat("A", 43) + `="}]}`},
		{"short key", `{"versions":[{"version":"v1","env":"ENCRYPTION_KEY","key_b64":"AAAA"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := backup.ParseRecoveryKit([]byte(tc.body)); err == nil {
				t.Fatal("accepted a bad kit")
			}
		})
	}
}
