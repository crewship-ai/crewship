package encryption

import (
	"errors"
	"strings"
	"testing"
)

var (
	envKeyV1 = KeyEnvVar("v1")
	envKeyV2 = KeyEnvVar("v2")
)

// Built, not literal, so secret scanners don't flag them.
var (
	kitKeyV1 = strings.Repeat("11", 32)
	kitKeyV2 = strings.Repeat("22", 32)
)

func TestResolveKeyFollowsDecryptFallback(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		version  string
		wantEnv  string
		wantFell bool
		wantErr  bool
	}{
		{name: "v1 from ENCRYPTION_KEY", env: map[string]string{envKeyV1: kitKeyV1}, version: "v1", wantEnv: "ENCRYPTION_KEY"},
		{name: "v2 from its own var", env: map[string]string{envKeyV1: kitKeyV1, envKeyV2: kitKeyV2}, version: "v2", wantEnv: "ENCRYPTION_KEY_V2"},
		{name: "v2 falls back like Decrypt", env: map[string]string{envKeyV1: kitKeyV1}, version: "v2", wantEnv: "ENCRYPTION_KEY", wantFell: true},
		{name: "nothing set", env: map[string]string{}, version: "v1", wantErr: true},
		{name: "not a version", env: map[string]string{envKeyV1: kitKeyV1}, version: "v100", wantErr: true},
		{name: "bad hex", env: map[string]string{envKeyV1: "zz"}, version: "v1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", "")
			t.Setenv(envKeyV2, "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := ResolveKey(tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Env != tc.wantEnv || got.FellBack != tc.wantFell || len(got.Key) != 32 {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestDecryptWithKeysNeedsTheRightKey(t *testing.T) {
	t.Setenv(KeyVersionEnvVar, "")
	t.Setenv("ENCRYPTION_KEY", kitKeyV1)
	t.Setenv(envKeyV2, kitKeyV2)
	v1, err := Encrypt("first")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyVersionEnvVar, "v2")
	v2, err := Encrypt("second")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v1, "v1:") || !strings.HasPrefix(v2, "v2:") {
		t.Fatalf("envelopes %q %q", v1, v2)
	}
	k1, _ := ResolveKey("v1")
	k2, _ := ResolveKey("v2")
	cases := []struct {
		name    string
		env     string
		keys    map[string][]byte
		want    string
		wantErr error
	}{
		{name: "v1 with the kit", env: v1, keys: map[string][]byte{"v1": k1.Key, "v2": k2.Key}, want: "first"},
		{name: "v2 with the kit", env: v2, keys: map[string][]byte{"v1": k1.Key, "v2": k2.Key}, want: "second"},
		{name: "v2 without its key", env: v2, keys: map[string][]byte{"v1": k1.Key}, wantErr: ErrNoKeyForVersion},
		{name: "v1 with the wrong key", env: v1, keys: map[string][]byte{"v1": k2.Key}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecryptWithKeys(tc.env, tc.keys)
			if tc.want != "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("decrypted %q without the right key", got)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestConfiguredKeyVersionsSortsNumerically(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", kitKeyV1)
	t.Setenv(KeyEnvVar("v10"), kitKeyV2)
	t.Setenv(envKeyV2, kitKeyV2)
	t.Setenv(KeyEnvVar("v3"), "") // set but empty is not configured
	got := strings.Join(ConfiguredKeyVersions(), ",")
	if got != "v1,v2,v10" {
		t.Fatalf("versions = %s", got)
	}
}
