package backup

import (
	"strings"
	"testing"

	"filippo.io/age"
)

// TestCreateOptionsValidate_EncryptionModes locks in the "exactly one"
// encryption mode contract. Without it, a caller can pass
// Passphrase + Recipients and hit the failure only after lock
// acquisition and .partial creation.
func TestCreateOptionsValidate_EncryptionModes(t *testing.T) {
	// The AGE recipient doesn't need to encrypt anything here; we only
	// care that the validator treats a non-empty Recipients slice as a
	// mode. Use a scrypt recipient derived from a throwaway passphrase
	// to avoid pulling in the X25519 identity machinery.
	rcpt, err := age.NewScryptRecipient("not-a-secret-just-a-test-pass")
	if err != nil {
		t.Fatalf("unexpected age recipient error: %v", err)
	}
	baseValid := CreateOptions{
		Scope:       ScopeWorkspace,
		WorkspaceID: "ws_1",
		Actor:       Actor{UserID: "u1", Role: "OWNER"},
	}

	cases := []struct {
		name       string
		mutate     func(*CreateOptions)
		wantErr    bool
		wantErrSub string
	}{
		{
			name:    "passphrase only — ok",
			mutate:  func(o *CreateOptions) { o.Passphrase = "hunter2" },
			wantErr: false,
		},
		{
			name:    "recipients only — ok",
			mutate:  func(o *CreateOptions) { o.Recipients = []age.Recipient{rcpt} },
			wantErr: false,
		},
		{
			name:    "no-encrypt only — ok",
			mutate:  func(o *CreateOptions) { o.NoEncrypt = true },
			wantErr: false,
		},
		{
			name:       "none — rejected",
			mutate:     func(o *CreateOptions) {},
			wantErr:    true,
			wantErrSub: "exactly one",
		},
		{
			name: "passphrase + recipients — rejected",
			mutate: func(o *CreateOptions) {
				o.Passphrase = "hunter2"
				o.Recipients = []age.Recipient{rcpt}
			},
			wantErr:    true,
			wantErrSub: "exactly one",
		},
		{
			name: "passphrase + no-encrypt — rejected",
			mutate: func(o *CreateOptions) {
				o.Passphrase = "hunter2"
				o.NoEncrypt = true
			},
			wantErr:    true,
			wantErrSub: "exactly one",
		},
		{
			name: "all three — rejected",
			mutate: func(o *CreateOptions) {
				o.Passphrase = "hunter2"
				o.Recipients = []age.Recipient{rcpt}
				o.NoEncrypt = true
			},
			wantErr:    true,
			wantErrSub: "exactly one",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			opts := baseValid
			tc.mutate(&opts)
			err := opts.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.wantErrSub != "" && !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("error %q did not contain %q", err.Error(), tc.wantErrSub)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
