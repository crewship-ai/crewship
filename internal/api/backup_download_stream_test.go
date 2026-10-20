package api

import (
	"bytes"
	"context"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/backup"
)

type backupDownloadWriter struct {
	*httptest.ResponseRecorder
	afterFirst func()
}

func (w *backupDownloadWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(body)
	if w.afterFirst != nil {
		change := w.afterFirst
		w.afterFirst = nil
		change()
	}
	return n, err
}

func TestBackupDownloadStopsAtAuthorityLoss(t *testing.T) {
	for _, scope := range []string{"workspace", "instance"} {
		for _, fault := range []string{"none", "session", "role", "suspended", "initial_role"} {
			t.Run(scope+"/"+fault, func(t *testing.T) {
				h, user, workspace, dir := covBk2Rig(t)
				t.Setenv(backup.InstanceOwnerEmailEnv, "")
				if scope == "instance" {
					execOrFatal(t, h.db, `UPDATE users SET instance_role='ADMIN' WHERE id=?`, user)
				}
				stamp := time.Now().UTC()
				path := filepath.Join(dir, backup.BundleFileName(backup.ScopeWorkspace, "test", stamp))
				payload := make([]byte, 128<<10)
				if _, err := rand.New(rand.NewSource(42)).Read(payload); err != nil {
					t.Fatal(err)
				}
				manifest := &backup.Manifest{FormatVersion: backup.FormatVersion, Scope: backup.ScopeWorkspace, CreatedAt: stamp,
					CompatibleTargets: []backup.Target{backup.TargetAnyInstance}, CreatedBy: backup.Actor{UserID: user},
					Contents: backup.Contents{Workspace: &backup.WorkspaceSummary{ID: workspace, Slug: "test", Name: "Test"}}}
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				writeErr := backup.WriteBundle(file, manifest, bytes.NewReader(payload), backup.WriteBundleOptions{NoEncrypt: true})
				closeErr := file.Close()
				if writeErr != nil || closeErr != nil {
					t.Fatalf("fixture write=%v close=%v", writeErr, closeErr)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() <= 32768 {
					t.Fatal("fixture must span multiple download chunks")
				}
				if err := backup.UpsertCatalogEntry(t.Context(), h.db, backup.CatalogEntry{FilePath: path, Scope: "workspace", WorkspaceID: workspace, CreatedAt: stamp, Size: info.Size(), FormatVersion: backup.FormatVersion}); err != nil {
					t.Fatal(err)
				}
				store := sessions.NewDBStore(h.db)
				session, err := store.Create(t.Context(), user, "download fixture", "", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				req := withWorkspaceUser(httptest.NewRequest("GET", "/?path="+url.QueryEscape(path), nil), user, workspace, "OWNER")
				if scope == "instance" {
					req = req.WithContext(context.WithValue(req.Context(), ctxInstanceAdmin, true))
				}
				registry := new(auth.StreamCredentials)
				middleware := &AuthMiddleware{sessions: store}
				ctx, release := registry.Track(req.Context(), registry.Epoch(), user, session.ID, "", stamp.Add(time.Hour), middleware.persistedSessionValidator(user, session.ID))
				defer release()
				req = req.WithContext(ctx)
				writer := &backupDownloadWriter{ResponseRecorder: httptest.NewRecorder()}
				if fault != "none" {
					writer.afterFirst = func() {
						switch fault {
						case "session":
							execOrFatal(t, h.db, `UPDATE user_sessions SET revoked_at=datetime('now') WHERE id=?`, session.ID)
						case "role", "initial_role":
							if scope == "instance" {
								execOrFatal(t, h.db, `UPDATE users SET instance_role=NULL WHERE id=?`, user)
							} else {
								execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, workspace, user)
							}
						case "suspended":
							execOrFatal(t, h.db, `UPDATE users SET suspended_at=datetime('now') WHERE id=?`, user)
						}
					}
				}
				if fault == "initial_role" {
					writer.afterFirst()
					writer.afterFirst = nil
				}
				if scope == "instance" {
					NewInstanceBackupsHandler(h.db, newTestLogger()).DownloadBundle(writer, req)
				} else {
					h.Download(writer, req)
				}
				if fault == "initial_role" {
					if writer.Code != http.StatusForbidden {
						t.Errorf("stale initial role returned HTTP%d want403", writer.Code)
					}
					if writer.Header().Get("Content-Type") == "application/zstd" {
						t.Error("denied initial download retained a bundle response")
					}
					return
				}
				if writer.Code != http.StatusOK || writer.Body.Len() == 0 {
					t.Fatalf("positive download admission failed: HTTP%d bytes%d", writer.Code, writer.Body.Len())
				}
				if fault == "none" && int64(writer.Body.Len()) != info.Size() {
					t.Errorf("healthy download=%d want%d", writer.Body.Len(), info.Size())
				}
				if fault != "none" && int64(writer.Body.Len()) >= info.Size() {
					t.Errorf("download continued after %s loss: bytes=%d total=%d", fault, writer.Body.Len(), info.Size())
				}
			})
		}
	}
}
