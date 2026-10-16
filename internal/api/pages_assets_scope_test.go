package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

func TestPageAssetRouterTokenScopeMatrix(t *testing.T) {
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, user)
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('scope-asset-page',?,'asset-scope','Assets',?,'{}')`, workspace, user)
	const secret = "synthetic-asset-scope-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(user, session.ID, "Synthetic", "synthetic@example.test")
	if err != nil {
		t.Fatal(err)
	}
	storage := storageDir(t)
	router, err := NewRouter(db, secret, newTestLogger(), WithStoragePath(storage))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	call := func(method, path, credential string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, bytes.NewReader(pageAssetPNG(t)))
		request.Header.Set("Authorization", "Bearer "+credential)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	prefix := "/api/v1/pages/asset-scope/assets"
	query := "?workspace_id=" + workspace
	created := call("POST", prefix+query+"&mutation_id=initial", token)
	artifactExpect(t, created, 201)
	asset := assetMeta(t, created)
	cases := []struct {
		name, method, tail string
		scopes             []string
		want               int
	}{
		{"read-cannot-upload", "POST", "", []string{"pages:read"}, 403},
		{"write-upload", "POST", "", []string{"pages:write"}, 201},
		{"wildcard-upload", "POST", "", []string{"pages:*"}, 201},
		{"legacy-unrestricted-upload", "POST", "", nil, 201},
		{"read-metadata", "GET", "/" + asset.ID, []string{"pages:read"}, 200},
		{"read-bytes", "GET", "/" + asset.ID + "/bytes", []string{"pages:read"}, 200},
		{"write-does-not-imply-read-metadata", "GET", "/" + asset.ID, []string{"pages:write"}, 403},
		{"write-does-not-imply-read-bytes", "GET", "/" + asset.ID + "/bytes", []string{"pages:write"}, 403},
		{"wildcard-bytes", "GET", "/" + asset.ID + "/bytes", []string{"pages:*"}, 200},
		{"legacy-unrestricted-bytes", "GET", "/" + asset.ID + "/bytes", nil, 200},
		{"unrelated-token-scope", "GET", "/" + asset.ID, []string{"agents:write"}, 403},
	}
	for i, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			credential := fmt.Sprintf("crewship_cli_asset_scope_%d", i)
			var scopes any
			if test.scopes != nil {
				raw, _ := json.Marshal(test.scopes)
				scopes = string(raw)
			}
			execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes,created_at) VALUES(?,?,'synthetic scope',?,'STANDARD',?,'2026-10-15T00:00:00Z')`, fmt.Sprintf("asset-scope-%d", i), user, hashStandard(credential), scopes)
			var before int
			if err = db.QueryRow(`SELECT count(*) FROM page_assets WHERE page_id='scope-asset-page'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			blobs := assetBlobFiles(t, storage)
			path := prefix + test.tail + query
			if test.method == "POST" {
				path += "&mutation_id=" + test.name
			}
			response := call(test.method, path, credential)
			artifactExpect(t, response, test.want)
			if test.method == "POST" && test.want == 403 {
				var after int
				if err = db.QueryRow(`SELECT count(*) FROM page_assets WHERE page_id='scope-asset-page'`).Scan(&after); err != nil || after != before || assetBlobFiles(t, storage) != blobs {
					t.Fatalf("scope refusal published rows/blobs: %d→%d %v", before, after, err)
				}
			}
		})
	}
	// The route table keeps Page authority in the handler while declaring the
	// independent token write ceiling; broad Page routes are not reclassified.
	for _, route := range router.mutationRoutes {
		if route.Method == "POST" && route.Pattern == "/api/v1/pages/{slug}/assets" && (route.Role != roleSelf || route.Scope != "pages:write") {
			t.Fatalf("asset policy=%+v", route)
		}
	}
}
