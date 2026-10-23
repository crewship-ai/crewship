package chataudience

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOriginalUserAuthorityRevisionExactJSONAndBounds(t *testing.T) {
	for _, value := range []string{"1", "9007199254740993", "9223372036854775807"} {
		if !ValidUserAuthorityRevision(value) {
			t.Fatal("exact SQLite integer refused", value)
		}
		raw, err := json.Marshal(Receipt{UserID: "human", UserAuthorityRevision: value})
		if err != nil || !strings.Contains(string(raw), `"user_authority_revision":"`+value+`"`) {
			t.Fatal(string(raw), err)
		}
		var got Receipt
		if json.Unmarshal(raw, &got) != nil || got.UserAuthorityRevision != value {
			t.Fatal("JSON rounded original fence", got)
		}
	}
	for _, raw := range []string{`{}`, `{"user_authority_revision":""}`, `{"user_authority_revision":"0"}`, `{"user_authority_revision":"01"}`, `{"user_authority_revision":"+1"}`, `{"user_authority_revision":"-1"}`, `{"user_authority_revision":"1.0"}`, `{"user_authority_revision":"1e3"}`, `{"user_authority_revision":"9223372036854775808"}`, `{"user_authority_revision":9007199254740993}`} {
		var receipt Receipt
		err := json.Unmarshal([]byte(raw), &receipt)
		if err == nil && ValidUserAuthorityRevision(receipt.UserAuthorityRevision) {
			t.Fatal("missing/invalid original fence admitted", raw)
		}
		if current, err := RevalidateOriginalUserQuery(t.Context(), nil, &receipt); err != nil || current != nil {
			t.Fatal("missing/invalid origin acquired authority", current, err)
		}
	}
}

func TestOriginalUserFenceRevokesCaptureAndPreservesLegacyIdentity(t *testing.T) {
	db := audienceDB(t)
	original, err := CaptureTrusted(t.Context(), db, "private", "author", nil)
	if err != nil || original == nil || !ValidUserAuthorityRevision(original.UserAuthorityRevision) {
		t.Fatal(original, err)
	}
	legacy := *original
	legacy.UserAuthorityRevision = ""
	current, err := CaptureTrusted(t.Context(), db, "private", "author", &legacy)
	if err != nil || current == nil || *current != legacy {
		t.Fatal("legacy retry was refreshed or broken", current, err)
	}
	if current, err := RevalidateOriginalUserQuery(t.Context(), db, &legacy); err != nil || current != nil {
		t.Fatal("legacy retry acquired new Mooring fence", current, err)
	}
	audienceExec(t, db, `UPDATE users SET access_revoked_at='2026-10-23T21:00:00Z' WHERE id='author'`)
	if current, err := RevalidateOriginalUserQuery(t.Context(), db, original); err != nil || current != nil {
		t.Fatal("old original receipt refreshed after revocation", current, err)
	}
	fresh, err := CaptureTrusted(t.Context(), db, "private", "author", nil)
	if err != nil || fresh == nil || fresh.UserAuthorityRevision == original.UserAuthorityRevision {
		t.Fatal("fresh admission lost revocation epoch", fresh, err)
	}
	if current, err := CaptureTrusted(t.Context(), db, "private", "author", &legacy); err != nil || current == nil || current.UserAuthorityRevision != "" {
		t.Fatal("legacy empty fence upgraded", current, err)
	}
}
