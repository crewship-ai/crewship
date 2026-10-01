package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// The Keeper defaults are a template: a workspace takes its own copy when it
// is created, whichever route creates it, and a later change of the defaults
// changes no existing workspace.

var templateDefaults = governance.Settings{Enabled: true, DenyNotifyMinRisk: 4, BehaviorSampleEvery: 10, AutoLeaseSeconds: 900}

func TestEveryWorkspaceCreationSeedsTheKeeperTemplate(t *testing.T) {
	// A source guard: each non-test file that inserts a workspace also seeds
	// its Keeper row, once per insert. A new creation path that forgets would
	// leave its workspaces on the built-in opt-out whatever the defaults say.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		inserts := strings.Count(src, "INSERT INTO workspaces (")
		if inserts == 0 {
			continue
		}
		checked++
		if seeds := strings.Count(src, "governance.SeedWorkspace("); seeds < inserts {
			t.Errorf("%s inserts %d workspace(s) but seeds the Keeper template %d time(s)", f, inserts, seeds)
		}
	}
	if checked < 3 {
		t.Fatalf("found %d files creating workspaces; the guard is not looking where it should", checked)
	}
}

func TestInstanceCreatedWorkspaceTakesACopyOfTheDefaults(t *testing.T) {
	f := newInstanceFixture(t)
	ctx := context.Background()
	if err := governance.SetDefaults(ctx, f.db, templateDefaults); err != nil {
		t.Fatal(err)
	}
	rr := f.do(f.boss, "POST", "/api/v1/admin/instance/workspaces", `{"name":"Ops","slug":"ops","owner_user_id":"carol"}`)
	wantCode(t, rr, http.StatusCreated, "create")
	id := decodeAs[struct {
		ID string `json:"id"`
	}](t, rr.Body.Bytes()).ID
	got, found := f.gov(id)
	if !found || got.Enabled != true || got.DenyNotifyMinRisk != 4 || got.BehaviorSampleEvery != 10 || got.AutoLeaseSeconds != 900 {
		t.Fatalf("new workspace = %+v (found %v), want its own copy of the defaults", got, found)
	}
	if err := governance.SetDefaults(ctx, f.db, governance.Settings{DenyNotifyMinRisk: 9}); err != nil {
		t.Fatal(err)
	}
	if after, _ := f.gov(id); after.DenyNotifyMinRisk != 4 || !after.Enabled {
		t.Fatalf("after the defaults changed the workspace reads %+v; a template must not reach back", after)
	}
}

func TestSelfServiceWorkspaceTakesACopyOfTheDefaults(t *testing.T) {
	db := setupTestDB(t)
	if err := governance.SetDefaults(context.Background(), db, templateDefaults); err != nil {
		t.Fatal(err)
	}
	userID := seedTestUser(t, db)
	h := NewWorkspaceHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest("POST", "/api/v1/workspaces", bytes.NewBufferString(`{"name":"Mine","slug":"mine"}`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rr.Code, rr.Body.String())
	}
	var ws struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &ws)
	got, found, err := governance.Get(context.Background(), db, ws.ID)
	if err != nil || !found || !got.Enabled || got.BehaviorSampleEvery != 10 {
		t.Fatalf("new workspace = %+v (found %v, err %v), want its own copy of the defaults", got, found, err)
	}
}
