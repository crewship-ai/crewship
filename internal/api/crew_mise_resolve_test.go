package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

type miseResolveFake struct {
	provider.ContainerProvider
	calls int
	image string
	cfg   *devcontainer.MiseConfig
	err   error
}

func (f *miseResolveFake) ResolveCrewMiseLock(_ context.Context, image string, cfg *devcontainer.MiseConfig, bump bool) (*devcontainer.MiseResolution, error) {
	f.calls++
	f.image = image
	f.cfg = cfg
	if f.err != nil {
		return nil, f.err
	}
	return &devcontainer.MiseResolution{ImageID: image, Platform: "linux-x64", MiseVersion: "2026.9.18", Lock: &devcontainer.MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "native"}}}, nil
}

func TestCrewMiseResolveAuthorityAndProposalOnly(t *testing.T) {
	for _, mode := range []string{"owner", "admin", "viewer", "manager", "agent", "no-source", "other-workspace", "deleted", "no-revision", "mutable", "unsafe-env", "unknown-image", "trailing", "oversized", "resolver-error", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			db := setupTestDB(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			crew := seedCrewRow(t, db, "resolver-crew", ws, "Resolver", "resolver")
			image := "sha256:" + strings.Repeat("a", 64)
			if _, err := db.Exec(`UPDATE crews SET cached_image=?,mise_config='{"tools":{"node":"22"}}' WHERE id=?`, image, crew); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO environment_revisions(id,workspace_id,crew_id,definition_hash,build_hash,image_id,toolchain_json,created_at) VALUES('resolve-revision',?,?, 'definition','build',?,'null',CURRENT_TIMESTAMP)`, ws, crew, image); err != nil {
				t.Fatal(err)
			}
			fake := &miseResolveFake{}
			h := newCrewMiseLockHandler(db, newTestLogger(), fake)
			role := "OWNER"
			kind := AuthKindCLIToken
			requestWS := ws
			body := `{"config":{"tools":{"node":"22"}},"bump":true}`
			want := 200
			switch mode {
			case "admin":
				role = "ADMIN"
			case "viewer":
				role = "VIEWER"
				want = 403
			case "manager":
				role = "MANAGER"
				want = 403
			case "agent":
				kind = ""
				want = 403
			case "no-source":
				want = 403
			case "other-workspace":
				requestWS = "other"
				want = 404
			case "deleted":
				db.Exec(`UPDATE crews SET deleted_at=CURRENT_TIMESTAMP WHERE id=?`, crew)
				want = 404
			case "no-revision":
				db.Exec(`DELETE FROM environment_revisions`)
				want = 409
			case "mutable":
				db.Exec(`UPDATE crews SET cached_image='crewship-cache:alias' WHERE id=?`, crew)
				want = 409
			case "unsafe-env":
				body = `{"config":{"tools":{"node":"22"},"env":{"PRIVATE":"must-not-forward"}}}`
				want = 400
			case "unknown-image":
				body = `{"image":"sha256:caller","config":{"tools":{"node":"22"}}}`
				want = 400
			case "trailing":
				body += "{}"
				want = 400
			case "oversized":
				body = strings.Repeat(" ", (4<<20)+1)
				want = 400
			case "resolver-error":
				fake.err = errors.New("private registry diagnostic")
				want = 502
			case "unavailable":
				h.resolver = nil
				want = 503
			}
			req := httptest.NewRequest("POST", "/resolve", strings.NewReader(body))
			req.SetPathValue("crewId", crew)
			source := auth.OriginalCredential{Kind: AuthKindCLIToken, ID: "token", UserID: user, Binding: "sha256:" + strings.Repeat("b", 64), UserAuthorityRevision: "1"}
			ctx := withUser(req.Context(), &AuthUser{ID: user, TokenID: "token", OriginalCredential: &source})
			ctx = withWorkspace(ctx, requestWS, role)
			ctx = context.WithValue(ctx, ctxAuthKind, kind)
			if mode != "no-source" {
				ctx = auth.WithOriginalCredential(ctx, source)
			}
			w := httptest.NewRecorder()
			h.Resolve(w, req.WithContext(ctx))
			if w.Code != want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private registry diagnostic") {
				t.Fatal("raw resolver error disclosed")
			}
			if want == 200 || mode == "resolver-error" {
				if fake.calls != 1 || fake.image != image || len(fake.cfg.Env) != 0 {
					t.Fatalf("unsafe call: %+v", fake)
				}
			} else if fake.calls != 0 {
				t.Fatal("denied request reached resolver")
			}
			var selected, mise string
			if err := db.QueryRow(`SELECT cached_image,mise_config FROM crews WHERE id=?`, crew).Scan(&selected, &mise); err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if json.Unmarshal([]byte(mise), &parsed) != nil || mise != `{"tools":{"node":"22"}}` {
				t.Fatal("proposal changed config")
			}
			if mode != "mutable" && selected != image {
				t.Fatal("proposal changed image")
			}
		})
	}
}
