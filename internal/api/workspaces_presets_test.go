package api

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestPresetCatalogAuthenticatedContract(t *testing.T) {
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	router, err := NewRouter(db, "preset-catalog-synthetic-secret-32chars", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	base := "/api/v1/workspaces/" + ws
	for i, scopes := range []any{nil, `["workspace:admin"]`, `["workspace:*"]`, `["*"]`, `["agents:write"]`, `[]`} {
		credential := "crewship_cli_catalog-" + strconv.Itoa(i)
		execOrFatal(t, db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,scopes) VALUES(?,?,?,?,?)`, "catalog-"+strconv.Itoa(i), owner, "Synthetic catalog scope", sha256Hex(credential), scopes)
		do := func(path string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+credential)
			r.Header.Set("X-Workspace-ID", ws)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			return w
		}
		w := do(base + "/access/presets")
		want := 200
		if i == 4 {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("scope%v got%d: %s", scopes, w.Code, w.Body.String())
		}
		if want == 200 {
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("catalog cacheable")
			}
			var result struct {
				Presets []access.CatalogEntry `json:"presets"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Presets) != 2 {
				t.Fatalf("catalog incomplete:%s %v", w.Body.String(), err)
			}
			for _, entry := range result.Presets {
				if entry.Hash != entry.Definition.Hash() {
					t.Fatal("definition hash differs")
				}
			}
			if w := do("/api/v1/workspaces/foreign/access/presets?workspace_id=" + ws); w.Code != 404 {
				t.Fatalf("foreign path accepted:%d", w.Code)
			}
		}
	}
	credential := "crewship_cli_catalog-0"
	do := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", base+"/access/presets", nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("X-Workspace-ID", ws)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=? AND workspace_id=?`, owner, ws)
	if w := do(); w.Code != 404 {
		t.Fatalf("demoted caller:%d", w.Code)
	}
	execOrFatal(t, db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id=? AND workspace_id=?`, owner, ws)
	if w := do(); w.Code == 200 {
		t.Fatal("restricted caller escaped ceiling")
	}
}
