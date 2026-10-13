package backup

import (
	"strings"
	"testing"
)

func TestForkInvitationUsesUnrecoverableHashAndVisibleReissueState(t *testing.T) {
	dump := &DBDump{Tables: map[string][]map[string]any{"workspace_invitations": {
		{"id": "pending", "token": "source-raw-pending", "expires_at": "2027-01-01T00:00:00Z", "accepted_at": nil},
		{"id": "accepted", "token": "source-raw-accepted", "expires_at": "2027-01-02T00:00:00Z", "accepted_at": "2026-01-01T00:00:00Z"},
	}}}
	counts, err := rekeyForkedCapabilityTokens(dump)
	if err != nil {
		t.Fatal(err)
	}
	if counts["workspace_invitations"] != 2 {
		t.Fatal(counts)
	}
	for _, row := range dump.Tables["workspace_invitations"] {
		stored := row["token"].(string)
		if !strings.HasPrefix(stored, "sha256:v1:") || len(stored) != 74 {
			t.Errorf("%s fork stores an invitation bearer proof, not tagged digest", row["id"])
		}
	}
	pending := dump.Tables["workspace_invitations"][0]
	if pending["expires_at"] != "1970-01-01T00:00:00Z" {
		t.Error("fork pending proof unavailable without visible expiry/reissue state")
	}
	accepted := dump.Tables["workspace_invitations"][1]
	if accepted["accepted_at"] != "2026-01-01T00:00:00Z" || accepted["expires_at"] != "2027-01-02T00:00:00Z" {
		t.Error("fork changed accepted history")
	}
}
