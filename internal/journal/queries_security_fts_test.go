package journal

import (
	"testing"
)

func TestSecurityAuditFTSPrivatePayloadBoundary(t *testing.T) {
	cases := []struct {
		name    string
		typ     EntryType
		payload map[string]any
	}{
		{"member-removed", EntryMemberRemoved, map[string]any{"previous_membership": map[string]any{"rights": []string{"securityprivateneedle"}}}},
	}
	for _, typ := range []EntryType{EntryInvitationCreated, EntryInvitationReissued, EntryInvitationRevoked, EntryInvitationAccepted} {
		cases = append(cases, struct {
			name    string
			typ     EntryType
			payload map[string]any
		}{string(typ), typ, map[string]any{"rights": []string{"securityprivateneedle"}, "access_mode": "securitymodeneedle"}})
	}
	for _, action := range []string{"user.session_revoked", "user.sessions_revoked", "user.unlocked"} {
		typ := EntryAuditEntityDeleted
		if action == "user.unlocked" {
			typ = EntryAuditEntityRestored
		}
		cases = append(cases, struct {
			name    string
			typ     EntryType
			payload map[string]any
		}{action, typ, map[string]any{"action": action, "metadata": map[string]any{"session_id": "securityprivateneedle", "old_locked": "securitymodeneedle"}}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDBWithFTS(t)
			defer db.Close()
			writer := NewWriter(db, quietLogger(), WriterOptions{})
			defer writer.Close()
			id, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: tc.typ, ActorType: ActorUser, Summary: "securitysummaryneedle", Payload: tc.payload})
			if err != nil {
				t.Fatal(err)
			}
			var before string
			if err := db.QueryRow(`SELECT payload FROM journal_entries WHERE id=?`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"securitysummaryneedle", "securityprivateneedle", "securitymodeneedle"} {
				q := Query{WorkspaceID: "ws_test", FTSQuery: query}
				rows, _, err := List(t.Context(), db, q)
				if err != nil {
					t.Fatal(err)
				}
				n, err := Count(t.Context(), db, q)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if query == "securitysummaryneedle" {
					want = 1
				}
				if len(rows) != want || n != int64(want) {
					t.Errorf("%s List%d Count%d want%d", query, len(rows), n, want)
				}
			}
			var after string
			if err := db.QueryRow(`SELECT payload FROM journal_entries WHERE id=?`, id).Scan(&after); err != nil || before != after {
				t.Fatal("FTS changed canonical payload")
			}
		})
	}
	// Other generic audits must retain payload search, even for older malformed
	// JSON or an absent/null action. JSON null must still match its summary.
	for _, typ := range []EntryType{EntryAuditEntityCreated, EntryAuditEntityUpdated, EntryAuditEntityDeleted, EntryAuditEntityRestored} {
		for _, raw := range []string{`{"action":"unrelated.action","marker":"securityprivateneedle"}`, `{"marker":"securityprivateneedle"}`, `{"action":null,"marker":"securityprivateneedle"}`, `invalid securityprivateneedle`, `null`} {
			t.Run(string(typ)+"/"+raw, func(t *testing.T) {
				db := openTestDBWithFTS(t)
				defer db.Close()
				writer := NewWriter(db, quietLogger(), WriterOptions{})
				defer writer.Close()
				id, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: typ, ActorType: ActorUser, Summary: "securitysummaryneedle"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE journal_entries SET payload=? WHERE id=?`, raw, id); err != nil {
					t.Fatal(err)
				}
				for _, query := range []string{"securitysummaryneedle", "securityprivateneedle"} {
					want := 1
					if raw == "null" && query == "securityprivateneedle" {
						want = 0
					}
					q := Query{WorkspaceID: "ws_test", FTSQuery: query}
					rows, _, err := List(t.Context(), db, q)
					n, countErr := Count(t.Context(), db, q)
					if err != nil || countErr != nil || len(rows) != want || n != int64(want) {
						t.Errorf("unrelated query%s List%d Count%d want%d err%v/%v", query, len(rows), n, want, err, countErr)
					}
				}
			})
		}
	}
}
