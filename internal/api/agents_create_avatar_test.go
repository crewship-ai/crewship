package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/avatarstyle"
)

// #3051: an agent created without avatar fields (CLI, API, Captain) must be
// stored with the same seed and style the web create dialog sends, instead of
// NULL until someone runs the backfill.
func TestCreateAgentStoresDefaultAvatarInputs(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantSeed  string
		wantStyle string
	}{
		{"omitted", `{"name":"Agent","slug":"a1","crew_id":"crew-ac"}`, "a1", avatarstyle.Default},
		{"null", `{"name":"Agent","slug":"a2","crew_id":"crew-ac","avatar_seed":null,"avatar_style":null}`, "a2", avatarstyle.Default},
		{"blank", `{"name":"Agent","slug":"a3","crew_id":"crew-ac","avatar_seed":"  ","avatar_style":""}`, "a3", avatarstyle.Default},
		{"explicit kept", `{"name":"Agent","slug":"a4","crew_id":"crew-ac","avatar_seed":"zed","avatar_style":"pixel-art"}`, "zed", "pixel-art"},
		{"partial", `{"name":"Agent","slug":"a5","crew_id":"crew-ac","avatar_style":"thumbs"}`, "a5", "thumbs"},
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
			if seed.String != tc.wantSeed || style.String != tc.wantStyle {
				t.Errorf("stored (%q, %q), want (%q, %q)", seed.String, style.String, tc.wantSeed, tc.wantStyle)
			}
			if resp.AvatarSeed == nil || *resp.AvatarSeed != tc.wantSeed || resp.AvatarStyle == nil || *resp.AvatarStyle != tc.wantStyle {
				t.Errorf("response avatar = (%v, %v), want (%q, %q)", resp.AvatarSeed, resp.AvatarStyle, tc.wantSeed, tc.wantStyle)
			}
		})
	}
}
