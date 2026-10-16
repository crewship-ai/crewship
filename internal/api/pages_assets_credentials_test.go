package api

import (
	"bytes"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

type assetCredentialBody struct {
	entered, release chan struct{}
	reader           io.Reader
}

func (b *assetCredentialBody) Read(p []byte) (int, error) {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-b.release
	return b.reader.Read(p)
}
func (b *assetCredentialBody) Close() error { return nil }

func TestPageAssetRouterRechecksExactCredentialAfterBody(t *testing.T) {
	for _, test := range []struct{ name, kind, change string }{
		{"valid-session", "session", ""}, {"valid-cli", "cli", ""},
		{"revoked-session", "session", "revoked"}, {"revoked-cli", "cli", "revoked"},
		{"expired-session", "session", "expired"}, {"expired-cli", "cli", "expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind := test.kind
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			workspace := seedTestWorkspace(t, db, user)
			execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('credential-asset-page',?,'asset-credential','Assets',?,'{}')`, workspace, user)
			const secret = "synthetic-asset-credential-secret-long-enough"
			validator, err := auth.NewJWTValidator(secret)
			if err != nil {
				t.Fatal(err)
			}
			var credential, credentialID string
			if kind == "session" {
				session, err := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
				if err != nil {
					t.Fatal(err)
				}
				credentialID = session.ID
				credential, err = validator.IssueAccessToken(user, session.ID, "Synthetic", "synthetic@example.test")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				credential = "crewship_cli_asset_delayed_test"
				credentialID = "asset-delayed-cli"
				// Existing upload is roleSelf/scopeSelf. The fix must not invent pages:write.
				execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES(?,?,'synthetic delayed',?,'STANDARD','["pages:read"]','2026-10-15T00:00:00Z')`, credentialID, user, hashStandard(credential))
			}
			storage := storageDir(t)
			router, err := NewRouter(db, secret, newTestLogger(), WithStoragePath(storage))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(router.Shutdown)
			body := &assetCredentialBody{entered: make(chan struct{}), release: make(chan struct{}), reader: bytes.NewReader(pageAssetPNG(t))}
			t.Cleanup(func() {
				select {
				case <-body.release:
				default:
					close(body.release)
				}
			})
			request := httptest.NewRequest("POST", "/api/v1/pages/asset-credential/assets?mutation_id=credential-test&workspace_id="+workspace, body)
			request.Header.Set("Authorization", "Bearer "+credential)
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); router.ServeHTTP(w, request); result <- w }()
			select {
			case <-body.entered:
			case w := <-result:
				t.Fatalf("credential never admitted: %d %s", w.Code, w.Body.String())
			case <-time.After(3 * time.Second):
				t.Fatal("body not reached")
			}
			table := "cli_tokens"
			if kind == "session" {
				table = "user_sessions"
			}
			switch test.change {
			case "revoked":
				execOrFatal(t, db, `UPDATE `+table+` SET revoked_at='2026-10-16T00:00:00Z' WHERE id=?`, credentialID)
			case "expired":
				execOrFatal(t, db, `UPDATE `+table+` SET expires_at='2020-01-01T00:00:00Z' WHERE id=?`, credentialID)
			}
			close(body.release)
			var w *httptest.ResponseRecorder
			select {
			case w = <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("handler did not finish after body release")
			}
			if test.change == "" {
				artifactExpect(t, w, 201)
				return
			}
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("changed %s accepted after body: %d %s", kind, w.Code, w.Body.String())
			}
			var rows int
			if err = db.QueryRow(`SELECT count(*) FROM page_assets WHERE page_id=?`, "credential-asset-page").Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("revoked upload rows=%d err=%v", rows, err)
			}
			if n := assetBlobFiles(t, storage); n != 0 {
				t.Fatalf("revoked upload published %d blobs", n)
			}
		})
	}
}
