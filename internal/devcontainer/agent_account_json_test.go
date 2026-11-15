package devcontainer

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// NormalizeAgentUserJSON is the stored form of a crew's devcontainer config:
// crew create, crew update and both apply planners run it, so the config a
// manifest declares and the config the server stores compare equal.
func TestNormalizeAgentUserJSON(t *testing.T) {
	const cu = "ghcr.io/devcontainers/features/common-utils:2"
	for _, tc := range []struct {
		name, in     string
		wantSame     bool
		wantAccount  bool
		wantKeys     []string
		wantErr      error
		wantOptValue map[string]string
		wantRaw      string
	}{
		{name: "bare image gets the account", in: `{"image":"debian:bookworm-slim"}`, wantAccount: true},
		{
			name:        "keys outside the model are kept",
			in:          `{"image":"debian:bookworm-slim","hostRequirements":{"memory":"4096mb"},"customizations":{"vscode":{}}}`,
			wantAccount: true, wantKeys: []string{"hostRequirements", "customizations"},
		},
		{
			name:        "declared feature gets its missing options and keeps the rest",
			in:          `{"image":"debian:bookworm-slim","features":{"` + cu + `":{"installZsh":"true"}}}`,
			wantAccount: true, wantOptValue: map[string]string{"installZsh": "true"},
		},
		{
			name:        "large numbers outside the model stay exact",
			in:          `{"image":"x","customizations":{"id":12345678901234567890}}`,
			wantAccount: true, wantKeys: []string{"customizations"}, wantRaw: `12345678901234567890`,
		},
		{name: "settled config is returned as is", in: `{"image":"x","features":{"` + cu + `":{"username":"agent","userUid":"1001","userGid":"1001"}}}`, wantSame: true},
		{name: "username none is the operator's choice", in: `{"image":"x","features":{"` + cu + `":{"username":"none"}}}`, wantSame: true},
		{name: "conflicting UID is refused", in: `{"image":"x","features":{"` + cu + `":{"userUid":"2000"}}}`, wantErr: ErrAgentAccountConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeAgentUserJSON(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantSame {
				if got != tc.in {
					t.Fatalf("settled config rewritten:\n in  %s\n out %s", tc.in, got)
				}
				return
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(got), &doc); err != nil {
				t.Fatalf("output is not JSON: %v: %s", err, got)
			}
			for _, k := range tc.wantKeys {
				if _, ok := doc[k]; !ok {
					t.Errorf("key %q dropped: %s", k, got)
				}
			}
			features, _ := doc["features"].(map[string]any)
			opts, _ := features[cu].(map[string]any)
			if tc.wantAccount && (opts["username"] != "agent" || opts["userUid"] != "1001" || opts["userGid"] != "1001") {
				t.Fatalf("no agent/1001/1001 account in %s", got)
			}
			if tc.wantRaw != "" && !strings.Contains(got, tc.wantRaw) {
				t.Errorf("output %s lost %s", got, tc.wantRaw)
			}
			for k, v := range tc.wantOptValue {
				if opts[k] != v {
					t.Errorf("option %s = %v, want %q kept", k, opts[k], v)
				}
			}
			again, err := NormalizeAgentUserJSON(got)
			if err != nil || again != got {
				t.Fatalf("not idempotent: %v\n first  %s\n second %s", err, got, again)
			}
		})
	}
}
