package auth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestOriginalCredentialSignedTicketAndCopies(t *testing.T) {
	r := OriginalCredential{UserID: "human", Kind: "session", ID: "session", ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), Binding: "sha256:" + strings.Repeat("a", 64), UserAuthorityRevision: "9007199254740993"}
	v, err := NewJWTValidator(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := v.IssueOriginalCredentialWSTicket(r.UserID, r.ID, "", "", "", r)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.ValidateWS(ticket)
	if err != nil || claims.OriginalCredential == nil || *claims.OriginalCredential != r {
		t.Fatal("signed original source changed", claims, err)
	}
	ctx := WithOriginalCredential(context.Background(), r)
	copy := OriginalCredentialFromContext(ctx)
	copy.UserAuthorityRevision = "1"
	if *OriginalCredentialFromContext(ctx) != r {
		t.Fatal("context shares mutable source")
	}
	for _, bad := range []struct{ user, sid, token string }{{"other", r.ID, ""}, {r.UserID, "other", ""}, {r.UserID, r.ID, "token"}, {r.UserID, "", r.ID}} {
		if _, err := v.IssueOriginalCredentialWSTicket(bad.user, bad.sid, bad.token, "", "", r); err == nil {
			t.Fatal("mismatched source signed", bad)
		}
	}
	legacy, err := v.IssueCredentialWSTicket(r.UserID, r.ID, "", "", "", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	old, err := v.ValidateWS(legacy)
	if err != nil || old.OriginalCredential != nil {
		t.Fatal("legacy ticket upgraded", err)
	}
}

func TestOriginalCredentialExactJSONAndBounds(t *testing.T) {
	r := OriginalCredential{UserID: "human", Kind: "cli_token", ID: "token", Binding: "sha256:" + strings.Repeat("a", 64), UserAuthorityRevision: "9223372036854775807"}
	if !r.Valid() {
		t.Fatal("valid nonexpiring CLI source rejected")
	}
	raw, _ := json.Marshal(r)
	var copy OriginalCredential
	if err := json.Unmarshal(raw, &copy); err != nil || copy != r {
		t.Fatal("round trip", err)
	}
	for _, revision := range []string{"", "0", "01", "+1", "-1", "1.0", "1e1", "9223372036854775808"} {
		copy = r
		copy.UserAuthorityRevision = revision
		if copy.Valid() {
			t.Fatal("invalid revision", revision)
		}
	}
	for _, change := range []func(*OriginalCredential){func(r *OriginalCredential) { r.Kind = "session" }, func(r *OriginalCredential) { r.ID = strings.Repeat("x", 201) }, func(r *OriginalCredential) { r.Binding = "sha256:" + strings.Repeat("A", 64) }, func(r *OriginalCredential) { r.ExpiresAt = "2030-01-01T01:00:00+01:00" }} {
		copy = r
		change(&copy)
		if copy.Valid() {
			t.Fatal("invalid source accepted", copy)
		}
	}
	if err := json.Unmarshal([]byte(`{"user_authority_revision":9007199254740993}`), &copy); err == nil {
		t.Fatal("numeric user fence accepted")
	}
}
