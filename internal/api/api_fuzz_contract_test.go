package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/image"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// The API fuzz leg validates live responses against the generated contract. A
// Go nil slice marshals to null, and these fields are declared as arrays, so
// an empty result broke every generated client. Each case is the empty state
// the fuzz run reached on stage.

func assertEmptyArray(t *testing.T, body []byte, path ...string) {
	t.Helper()
	var cur any
	if err := json.Unmarshal(body, &cur); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%v: %q is not reachable in %s", path, key, body)
		}
		cur, ok = obj[key]
		if !ok {
			t.Fatalf("%v: %q is missing in %s", path, key, body)
		}
	}
	arr, ok := cur.([]any)
	if !ok || len(arr) != 0 {
		t.Fatalf("%v = %#v, want an empty array (%s)", path, cur, body)
	}
}

func TestInstanceKeeperGovernance_EmptyPresetsAreArrays(t *testing.T) {
	f := newInstanceFixture(t)

	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/keeper/governance/defaults", "")
	wantCode(t, rr, http.StatusOK, "defaults")
	assertEmptyArray(t, rr.Body.Bytes(), "defaults", "watch_presets")

	rr = f.do(f.boss, "GET", "/api/v1/admin/instance/keeper/governance", "")
	wantCode(t, rr, http.StatusOK, "list")
	assertEmptyArray(t, rr.Body.Bytes(), "defaults", "watch_presets")
	if strings.Contains(rr.Body.String(), `"watch_presets":null`) {
		t.Fatalf("a workspace row carries null watch_presets: %s", rr.Body.String())
	}
}

func TestKeeperGovernance_EmptyPresetsAreArrays(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewKeeperGovernanceHandler(db, newComposioTestLogger(), nil)

	rr := doGovernanceReq(t, h, http.MethodGet, "", wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertEmptyArray(t, rr.Body.Bytes(), "watch_presets")
}

func TestJudgeModels_FailureAnswersCarryAnEmptyList(t *testing.T) {
	for name, endpoint := range map[string]string{
		"invalid endpoint":     "://nope",
		"unreachable endpoint": "http://127.0.0.1:1",
	} {
		t.Run(name, func(t *testing.T) {
			h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
			req := httptest.NewRequest("GET", "/api/v1/admin/keeper/judge/models", nil)
			q := req.URL.Query()
			q.Set("endpoint", endpoint)
			req.URL.RawQuery = q.Encode()
			req = req.WithContext(context.WithValue(req.Context(), ctxRole, "OWNER"))
			rr := httptest.NewRecorder()
			h.Models(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			assertEmptyArray(t, rr.Body.Bytes(), "models")
		})
	}
}

func TestCatalogSearchWithoutMatchAnswersEmptyArrays(t *testing.T) {
	h := newTestProvisioningHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/features/catalog?search=no-such-feature-xyz", nil)
	rr := httptest.NewRecorder()
	h.CatalogList(rr, req)
	assertEmptyArray(t, rr.Body.Bytes(), "features")

	req = httptest.NewRequest("GET", "/api/v1/runtimes/catalog?search=no-such-runtime-xyz", nil)
	rr = httptest.NewRecorder()
	h.RuntimeCatalogList(rr, req)
	assertEmptyArray(t, rr.Body.Bytes(), "runtimes")
}

func TestFilterHelpersNeverReturnNil(t *testing.T) {
	if got := devcontainer.FilterCatalog(nil, "x"); got == nil {
		t.Error("FilterCatalog returned nil")
	}
	if got := devcontainer.FilterRuntimes(nil, "x"); got == nil {
		t.Error("FilterRuntimes returned nil")
	}
}

func TestMemoryVersions_UnknownPathAnswersEmptyEntries(t *testing.T) {
	h, _, userID, wsID, _ := newMemVerHandlerTest(t)
	req := httptest.NewRequest("GET", "/api/v1/memory/versions?path=crew:c1/never-written.md", nil)
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertEmptyArray(t, rr.Body.Bytes(), "entries")
}

func TestCacheArtifactReferences_UnreferencedImageIsEmptyNotNil(t *testing.T) {
	got := cacheArtifactReferences(map[string][]string{}, image.Summary{ID: "sha256:a", RepoTags: []string{"crewship-cache:a"}})
	if got == nil || len(got) != 0 {
		t.Fatalf("references = %#v, want an empty non-nil slice", got)
	}
	body, err := json.Marshal(CacheImageInfo{ReferencedBy: got})
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyArray(t, body, "referenced_by")
}

// The schema enum and the column's CHECK agree on three values. A new agent
// takes the column default, so the create answer has to say so too rather than
// the struct's empty string.
func TestCreateAgent_ReportsTheStoredRestrictedProfile(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-1', ?, 'Devs', 'devs')`, wsID); err != nil {
		t.Fatal(err)
	}
	h := NewAgentHandler(db, logger)

	req := httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader(`{"name":"Fuzz Bot","slug":"fuzz-bot","crew_id":"crew-1"}`))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		Profile string `json:"restricted_execution_profile"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow(`SELECT restricted_execution_profile FROM agents WHERE id=?`, created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if created.Profile != stored {
		t.Fatalf("create answered %q, the row holds %q", created.Profile, stored)
	}
}

// A crew agent with an empty directory answers {"files":null}.
func TestCrewFiles_NullFromTheCrewAgentIsAnEmptyList(t *testing.T) {
	sock := newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":null}`))
	}))
	h, userID, wsID, crewID, _ := covProxyRig(t, sock)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CrewFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	assertEmptyArray(t, rr.Body.Bytes())
}
