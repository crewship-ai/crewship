package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// API↔CLI parity for #1669: every route added under /api/v1/users/me/
// user-model is driven here through the command the agent and the
// operator actually use, not a hand-rolled request.

func TestPrivacyUserModelListRunE_RendersEveryStoredField(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/users/me/user-model", clitest.JSONResponse(200, userModelResponse{
		UserID: "u1",
		Exists: true,
		Facts: []userModelFact{
			{Key: "role", Value: "runs the platform team"},
			{Key: "constraint", Value: "commits carry no co-author trailer"},
		},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelListCmd.RunE(privacyUserModelListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"role", "runs the platform team", "constraint", "co-author trailer"} {
		if !strings.Contains(out, want) {
			t.Errorf("user-model list omitted %q:\n%s", want, out)
		}
	}
}

// An empty model must render as an empty readout rather than an error —
// "nothing is stored about you" is the honest answer for a fresh
// operator, and it is the answer the issue's live probe got.
func TestPrivacyUserModelListRunE_EmptyModelIsNotAnError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/users/me/user-model", clitest.JSONResponse(200, userModelResponse{
		UserID: "u1", Exists: false, Facts: []userModelFact{},
	}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelListCmd.RunE(privacyUserModelListCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

func TestPrivacyUserModelForgetRunE_TargetsTheNamedField(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/users/me/user-model/facts/timezone",
		clitest.JSONResponse(200, userModelResponse{
			Forgot:    "timezone",
			Remaining: []userModelFact{{Key: "role", Value: "runs the platform team"}},
		}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelForgetCmd.RunE(privacyUserModelForgetCmd, []string{"timezone"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/users/me/user-model/facts/timezone")); n != 1 {
		t.Errorf("expected 1 DELETE on the named field, got %d", n)
	}
}

// A field name with a space must reach the field route as one path
// segment rather than becoming a malformed request line. url.PathEscape
// is what makes that true; the stub sees the decoded path.
func TestPrivacyUserModelForgetRunE_EscapesTheFieldName(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/users/me/user-model/facts/odd name",
		clitest.JSONResponse(200, userModelResponse{Forgot: "odd name"}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelForgetCmd.RunE(privacyUserModelForgetCmd, []string{"odd name"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/users/me/user-model")); n != 0 {
		t.Errorf("the field name collapsed into the whole-model delete route")
	}
}

func TestPrivacyUserModelDeleteRunE_ConfirmsThenPurges(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/users/me/user-model", clitest.JSONResponse(200, userModelResponse{Purged: 1}))
	covSetupCli10(t, s.URL())
	_ = privacyUserModelDeleteCmd.Flags().Set("yes", "true")
	t.Cleanup(func() { _ = privacyUserModelDeleteCmd.Flags().Set("yes", "false") })

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelDeleteCmd.RunE(privacyUserModelDeleteCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/users/me/user-model")); n != 1 {
		t.Errorf("expected 1 DELETE, got %d", n)
	}
}

// #1693 — `--provenance` renders where each entry came from, and a fact
// the server has no evidence for renders '-' rather than an invented
// origin. `show <field>` narrows the readout to one field, and a field
// that is not stored is a miss, not an empty table.
func TestPrivacyUserModelListRunE_ProvenanceColumns(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/users/me/user-model", clitest.JSONResponse(200, userModelResponse{
		UserID: "u1",
		Exists: true,
		Facts: []userModelFact{
			{Key: "role", Value: "runs the platform team", Provenance: &userModelProvenance{
				Quote: "I run the platform team here", MessageID: "msg-1", SourceType: "stated", At: "2026-09-15T05:00:00.000Z"}},
			{Key: "timezone", Value: "UTC+1"}, // recorded before provenance existed
		},
	}))
	covSetupCli10(t, s.URL())

	cases := []struct {
		name       string
		args       []string
		provenance bool
		want       []string
		wantAbsent []string
		wantErr    string
	}{
		{
			name:       "without the flag the table is unchanged",
			want:       []string{"FIELD", "VALUE", "role", "timezone"},
			wantAbsent: []string{"QUOTE", "I run the platform team here", "msg-1"},
		},
		{
			name:       "with the flag every entry says where it came from",
			provenance: true,
			want:       []string{"QUOTE", "RECORDED", "MESSAGE", "I run the platform team here", "msg-1", "2026-09-15T05:00:00.000Z", "stated"},
		},
		{
			name:       "an entry without evidence renders a dash, not a fabricated origin",
			provenance: true,
			want:       []string{"timezone", "UTC+1", "-"},
		},
		{
			name:       "show <field> narrows to one entry",
			args:       []string{"role"},
			provenance: true,
			want:       []string{"role", "msg-1"},
			wantAbsent: []string{"timezone"},
		},
		{
			name:    "show <field> for a field that is not stored is a miss",
			args:    []string{"language"},
			wantErr: `no field "language"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = privacyUserModelListCmd.Flags().Set("provenance", "false")
			if tc.provenance {
				_ = privacyUserModelListCmd.Flags().Set("provenance", "true")
			}
			t.Cleanup(func() { _ = privacyUserModelListCmd.Flags().Set("provenance", "false") })

			out, err := captureStdoutCovCli10(t, func() error {
				return privacyUserModelListCmd.RunE(privacyUserModelListCmd, tc.args)
			})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RunE: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output omitted %q:\n%s", want, out)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(out, absent) {
					t.Errorf("output should not contain %q:\n%s", absent, out)
				}
			}
		})
	}
}
