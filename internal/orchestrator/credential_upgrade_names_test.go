package orchestrator

import "testing"

func TestCredentialUpgradeNamesMetadataOnly(t *testing.T) {
	for _, typ := range []string{"SSH_KEY", "CERTIFICATE"} {
		creds := []Credential{{ID: "file", Type: typ, EnvVarName: "KEY"}, {ID: "other", Type: "GENERIC_SECRET", EnvVarName: "KEY_PATH"}}
		p := InspectCredentialUpgradeNames(creds, true, nil)
		if len(p) != 1 || p[0].Alias != "KEY_PATH" || len(p[0].CredentialIDs) != 2 {
			t.Fatalf("%s alias conflict missed: %+v", typ, p)
		}
	}
	cases := []struct {
		name   string
		creds  []Credential
		keeper bool
		refs   map[string]bool
		want   string
	}{
		{"same-credential-alias-part", []Credential{{ID: "one", Type: "SSH_KEY", EnvVarName: "KEY", Fields: []CredentialField{{EnvVar: "KEY_PATH"}}}}, true, nil, "potential_collision"},
		{"reserved-oauth", []Credential{{ID: "one", Type: "OAUTH2", EnvVarName: "PATH"}}, true, nil, "reserved_runtime_name"},
		{"reserved-oauth-part", []Credential{{ID: "one", Type: "OAUTH2", EnvVarName: "ACCESS", Fields: []CredentialField{{EnvVar: "LD_AUDIT"}}}}, true, nil, "reserved_runtime_name"},
		{"reserved-cli-token-part", []Credential{{ID: "one", Type: "CLI_TOKEN", EnvVarName: "OPENAI", Fields: []CredentialField{{EnvVar: "OPENAI_API_KEY"}}}}, true, nil, "reserved_runtime_name"},
		{"duplicate-slots", []Credential{{ID: "one", Type: "GENERIC_SECRET", EnvVarName: "SLOT"}, {ID: "two", Type: "GENERIC_SECRET", EnvVarName: "SLOT"}}, true, nil, "potential_collision"},
		{"userpass", []Credential{{ID: "one", Type: "USERPASS", EnvVarName: "LOGIN"}, {ID: "two", Type: "GENERIC_SECRET", EnvVarName: "LOGIN_PASSWORD"}}, true, nil, "potential_collision"},
		{"reserved", []Credential{{ID: "one", Type: "GENERIC_SECRET", EnvVarName: "PATH"}}, true, nil, "reserved_runtime_name"},
		{"unknown", []Credential{{ID: "one", Type: "UNCLASSIFIED", EnvVarName: "SLOT"}}, true, nil, "unsupported_type"},
		{"keeper-withholds", []Credential{{ID: "one", Type: "SECRET", EnvVarName: "SLOT"}, {ID: "two", Type: "GENERIC_SECRET", EnvVarName: "SLOT"}}, true, nil, ""},
		{"keeper-off", []Credential{{ID: "one", Type: "SECRET", EnvVarName: "SLOT"}, {ID: "two", Type: "GENERIC_SECRET", EnvVarName: "SLOT"}}, false, nil, "potential_collision"},
		{"mcp-extra-path", []Credential{{ID: "one", Type: "GENERIC_SECRET", EnvVarName: "SLOT"}, {ID: "two", Type: "GENERIC_SECRET", EnvVarName: "SLOT_PATH"}}, true, map[string]bool{"SLOT": true}, "potential_collision"},
		{"part", []Credential{{ID: "one", Type: "SSH_KEY", EnvVarName: "KEY", Fields: []CredentialField{{EnvVar: "KEY_PART"}}}, {ID: "two", Type: "GENERIC_SECRET", EnvVarName: "KEY_PART"}}, true, nil, "potential_collision"},
		{"keeper-identifier", []Credential{{ID: "one", Type: "SECRET", EnvVarName: "SLOT", Fields: []CredentialField{{EnvVar: "ACCOUNT"}}}, {ID: "two", Type: "GENERIC_SECRET", EnvVarName: "ACCOUNT"}}, true, nil, "potential_collision"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := InspectCredentialUpgradeNames(tc.creds, tc.keeper, tc.refs)
			if tc.want == "" {
				if len(p) > 0 {
					t.Fatalf("unexpected problem: %+v", p)
				}
				return
			}
			for _, problem := range p {
				if problem.Reason == tc.want {
					return
				}
			}
			t.Fatalf("missing %s: %+v", tc.want, p)
		})
	}
}
