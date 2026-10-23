package chatbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestOriginalCredentialIPCProvenanceAndResumePrecedence(t *testing.T) {
	source := auth.OriginalCredential{UserID: "human", Kind: "cli_token", ID: "cli", Binding: "sha256:" + strings.Repeat("a", 64), UserAuthorityRevision: "9007199254740993"}
	receipt := &chataudience.Receipt{UserID: source.UserID, UserAuthorityRevision: source.UserAuthorityRevision, SourceCredentialKind: source.Kind, SourceCredentialID: source.ID, SourceCredentialBinding: source.Binding}
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Internal-Token") != "master-fixture" {
			t.Error("missing host authentication")
		}
		query := r.URL.Query()
		if calls == 1 {
			raw, err := base64.RawURLEncoding.Strict().DecodeString(query.Get("source_credential"))
			if err != nil {
				t.Error(err)
			}
			var got auth.OriginalCredential
			if err := json.Unmarshal(raw, &got); err != nil || got != source {
				t.Error("original IPC source changed", got, err)
			}
			if query.Get("receipt") != "" {
				t.Error("source request unexpectedly has queued receipt")
			}
		} else if query.Get("source_credential") != "" || query.Get("receipt") == "" {
			t.Error("resume source replaced original queued receipt")
		}
		json.NewEncoder(w).Encode(chatResolveResponse{HumanAuthority: receipt})
	}))
	defer ts.Close()
	resolver := NewIPCResolver(ts.URL, "master-fixture", slog.Default())
	ctx := auth.WithOriginalCredential(context.Background(), source)
	if _, err := resolver.ResolveHumanChat(ctx, source.UserID, "chat"); err != nil {
		t.Fatal(err)
	}
	ctx = chataudience.WithReceipt(ctx, receipt)
	if _, err := resolver.ResolveHumanChat(ctx, source.UserID, "chat"); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveHumanChat(auth.WithOriginalCredential(context.Background(), source), "other", "chat"); err == nil {
		t.Fatal("source rebound to another sender")
	}
	if calls != 2 {
		t.Fatal("invalid source reached IPC", calls)
	}
}

func TestOriginalCredentialCannotComeFromPublicMessageJSON(t *testing.T) {
	var option ws.ChatMessageOption
	if err := json.Unmarshal([]byte(`{"OriginalCredential":{"user_id":"attacker","kind":"cli_token","id":"cli","binding":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","user_authority_revision":"1"},"original_credential":{"user_id":"attacker"},"HumanAuthority":{"user_id":"attacker"}}`), &option); err != nil {
		t.Fatal(err)
	}
	if option.OriginalCredential != (auth.OriginalCredential{}) || option.HumanAuthority != nil {
		t.Fatal("public JSON populated host provenance")
	}
}
