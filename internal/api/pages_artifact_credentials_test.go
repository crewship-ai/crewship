package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

type staticArtifactBodySignal struct {
	io.ReadCloser
	once    sync.Once
	entered chan struct{}
}

func (b *staticArtifactBodySignal) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	return b.ReadCloser.Read(p)
}

func TestStaticArtifactHTTPExactCredentialRevokedDuringBodyAndRetry(t *testing.T) {
	for _, mode := range []string{"session", "CLI-revoked", "CLI-scope"} {
		t.Run(mode, func(t *testing.T) {
			f := newStaticArtifactHTTPFixture(t)
			token := f.token
			sessionID := ""
			if mode == "session" {
				session, err := sessions.NewDBStore(f.router.db).Create(t.Context(), f.owner, "Static Artifact real session", "127.0.0.1", auth.RefreshTokenTTL)
				if err != nil {
					t.Fatal(err)
				}
				sessionID = session.ID
				validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
				if err != nil {
					t.Fatal(err)
				}
				token, err = validator.IssueAccessToken(f.owner, session.ID, "Owner", "owner@example.invalid")
				if err != nil {
					t.Fatal(err)
				}
			} else {
				// Another valid credential for the same user must not replace the original.
				if _, err := f.router.db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,created_at) VALUES('static-alternative',?,'Alternative',?,'STANDARD',datetime('now'))`, f.owner, sha256Hex("crewship_cli_static-alternative-fixture")); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Body = &staticArtifactBodySignal{ReadCloser: r.Body, entered: entered}
				f.router.ServeHTTP(w, r)
			}))
			defer server.Close()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			path := "/api/v1/pages/report/artifact/revisions"
			req, err := http.NewRequestWithContext(ctx, "POST", server.URL+path+"?workspace_id="+f.ws, reader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			done := make(chan *http.Response, 1)
			requestErr := make(chan error, 1)
			go func() {
				resp, err := server.Client().Do(req)
				if err != nil {
					requestErr <- err
					return
				}
				done <- resp
			}()
			select {
			case <-entered:
			case err := <-requestErr:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal("handler did not enter body read")
			}
			var changed int64
			switch mode {
			case "session":
				if err := sessions.NewDBStore(f.router.db).Revoke(ctx, sessionID, sessions.ReasonAdminForce); err != nil {
					t.Fatal(err)
				}
				if err := f.router.db.QueryRow(`SELECT COUNT(*) FROM user_sessions WHERE id=? AND user_id=? AND revoked_at IS NOT NULL`, sessionID, f.owner).Scan(&changed); err != nil {
					t.Fatal(err)
				}
			case "CLI-revoked":
				result, err := f.router.db.Exec(`UPDATE cli_tokens SET revoked_at=datetime('now') WHERE user_id=? AND name='t'`, f.owner)
				if err != nil {
					t.Fatal(err)
				}
				changed, _ = result.RowsAffected()
			case "CLI-scope":
				result, err := f.router.db.Exec(`UPDATE cli_tokens SET scopes='["pages:read"]' WHERE user_id=? AND name='t'`, f.owner)
				if err != nil {
					t.Fatal(err)
				}
				changed, _ = result.RowsAffected()
			}
			if changed != 1 {
				t.Fatalf("exact credential change affected%d rows", changed)
			}
			body := f.mutation("<p>Must not publish</p>", "revoked-body", 0, 0)
			if _, err = io.WriteString(writer, body); err != nil {
				t.Fatal(err)
			}
			writer.Close()
			select {
			case resp := <-done:
				raw, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				staticArtifactStatus(t, resp.StatusCode, raw, 403)
			case err := <-requestErr:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal("request did not finish after credential revocation")
			}
			status, raw := f.call(t, "POST", path, token, body)
			want := 401
			if mode == "CLI-scope" {
				want = 403
			}
			staticArtifactStatus(t, status, raw, want)
			var n int
			if err = f.router.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, f.pageID).Scan(&n); err != nil || n != 0 {
				t.Fatalf("revoked caller published%d %v", n, err)
			}
			if mode == "CLI-scope" {
				status, raw = f.call(t, "GET", "/api/v1/pages/report/artifact?page_id="+f.pageID, token, "")
				staticArtifactStatus(t, status, raw, 200)
				var result struct {
					CanWrite bool `json:"can_write"`
				}
				if err = json.Unmarshal(raw, &result); err != nil {
					t.Fatal(err)
				}
				if result.CanWrite {
					t.Fatal("owner read-only token advertised write")
				}
			}
		})
	}
}
