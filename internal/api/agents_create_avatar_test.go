package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// assertAgentsFollowCrewStyle is the #3051 contract for every agent-creating
// path: the seed is pinned to the slug, and the style stays NULL so the agent
// keeps following its crew's style (NULL is the documented "follow the crew"
// state). It also fails when no agent row exists, so a test that never created
// one cannot pass.
func assertAgentsFollowCrewStyle(t *testing.T, db *sql.DB) {
	t.Helper()
	var total, bad int
	if err := db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN avatar_seed IS NULL OR avatar_seed <> slug OR avatar_style IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM agents`).Scan(&total, &bad); err != nil {
		t.Fatalf("read agents: %v", err)
	}
	if total == 0 {
		t.Fatal("no agent rows to check")
	}
	if bad != 0 {
		t.Errorf("%d of %d agents lack avatar_seed=slug with NULL avatar_style", bad, total)
	}
}

// #3051: an agent created without avatar fields (CLI, API) is stored with its
// slug as seed and keeps a NULL style (follow the crew); chosen values stay.
func TestCreateAgentStoresDefaultAvatarSeed(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantSeed  string
		wantStyle string // "" = NULL
	}{
		{"omitted", `{"name":"Agent","slug":"a1","crew_id":"crew-ac"}`, "a1", ""},
		{"null", `{"name":"Agent","slug":"a2","crew_id":"crew-ac","avatar_seed":null,"avatar_style":null}`, "a2", ""},
		{"blank seed", `{"name":"Agent","slug":"a3","crew_id":"crew-ac","avatar_seed":"  "}`, "a3", ""},
		{"explicit kept", `{"name":"Agent","slug":"a4","crew_id":"crew-ac","avatar_seed":"zed","avatar_style":"pixel-art"}`, "zed", "pixel-art"},
		{"style only", `{"name":"Agent","slug":"a5","crew_id":"crew-ac","avatar_style":"thumbs"}`, "a5", "thumbs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newCovACRig(t)
			rec := httptest.NewRecorder()
			r.h.Create(rec, r.req(t, "ADMIN", tc.body))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
			}
			var resp agentResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			var seed, style sql.NullString
			if err := r.db.QueryRow(`SELECT avatar_seed, avatar_style FROM agents WHERE id = ?`, resp.ID).Scan(&seed, &style); err != nil {
				t.Fatal(err)
			}
			if seed.String != tc.wantSeed || style.String != tc.wantStyle || style.Valid != (tc.wantStyle != "") {
				t.Errorf("stored (%q, %v), want (%q, %q)", seed.String, style, tc.wantSeed, tc.wantStyle)
			}
		})
	}
}

// A crew with its own style: an API-created agent keeps following it, and a
// later crew style change reaches it (the effective style is the COALESCE the
// renderer and group chat use).
func TestCreateAgentFollowsCrewStyle(t *testing.T) {
	r := newCovACRig(t)
	if _, err := r.db.Exec(`UPDATE crews SET avatar_style = 'pixel-art' WHERE id = ?`, r.crewID); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req(t, "ADMIN", `{"name":"Agent","slug":"follow","crew_id":"crew-ac"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	effective := func() string {
		var s string
		if err := r.db.QueryRow(`SELECT COALESCE(a.avatar_style, cr.avatar_style, '') FROM agents a JOIN crews cr ON cr.id = a.crew_id WHERE a.slug = 'follow'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := effective(); got != "pixel-art" {
		t.Fatalf("effective style = %q, want pixel-art", got)
	}
	if _, err := r.db.Exec(`UPDATE crews SET avatar_style = 'thumbs' WHERE id = ?`, r.crewID); err != nil {
		t.Fatal(err)
	}
	if got := effective(); got != "thumbs" {
		t.Errorf("effective style after crew change = %q, want thumbs", got)
	}
}
