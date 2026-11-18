package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The sidecar no longer rejects targets using its stale boot snapshot. These
// are the real host-handler boundaries that must remain authoritative instead:
// an old but valid crew token cannot dispatch a deleted/moved actor or target.
func TestDelegationLiveMembershipRefusesRevokedAndForeignAgents(t *testing.T) {
	for _, kind := range []string{"assignment", "query"} {
		for _, who := range []string{"actor", "target"} {
			for _, change := range []string{"deleted", "other_crew", "other_workspace"} {
				t.Run(kind+"/"+who+"/"+change, func(t *testing.T) {
					var db *sql.DB
					var ws, crew, actor, target, chat, slug string
					var handler http.HandlerFunc
					if kind == "assignment" {
						h, w, c, a, to, ch := covAsgRig(t)
						db = h.db
						ws, crew, actor, target, chat, slug = w, c, a, to, ch, "asg-worker"
						handler = h.Create
					} else {
						h, w, c, a, to, ch := covQryRig(t)
						db = h.db
						ws, crew, actor, target, chat, slug = w, c, a, to, ch, "qry2-to"
						handler = h.Create
					}
					id := actor
					if who == "target" {
						id = target
					}
					switch change {
					case "deleted":
						execOrFatal(t, db, `UPDATE agents SET deleted_at=datetime('now') WHERE id=?`, id)
					case "other_crew":
						foreignCrew := seedCrewRow(t, db, "delegation-foreign-crew", ws, "FRN", "delegation-foreign")
						execOrFatal(t, db, `UPDATE agents SET crew_id=? WHERE id=?`, foreignCrew, id)
					case "other_workspace":
						foreignWS := "delegation-foreign-ws"
						execOrFatal(t, db, `INSERT INTO workspaces (id,name,slug) VALUES (?,'Foreign','delegation-foreign-ws')`, foreignWS)
						foreignCrew := seedCrewRow(t, db, "delegation-foreign-crew", foreignWS, "FRN", "delegation-foreign")
						execOrFatal(t, db, `UPDATE agents SET workspace_id=?,crew_id=? WHERE id=?`, foreignWS, foreignCrew, id)
					}
					raw, _ := json.Marshal(map[string]string{"actor_agent_id": actor, "target_slug": slug, "task": "work", "question": "question", "crew_id": crew, "workspace_id": ws, "chat_id": chat})
					req := withInternalCrewToken(httptest.NewRequest(http.MethodPost, "/internal/"+kind, strings.NewReader(string(raw))), ws, crew)
					rr := httptest.NewRecorder()
					handler(rr, req)
					if rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
						t.Fatalf("revoked/foreign %s admitted or failed unclearly: %d %s", who, rr.Code, rr.Body.String())
					}
					for _, table := range []string{"assignments", "peer_conversations"} {
						var n int
						if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
							t.Fatal(err)
						}
						if n != 0 {
							t.Fatalf("unauthorized dispatch wrote %d %s", n, table)
						}
					}
				})
			}
		}
	}
}

func TestQueryLiveMembershipSeesNewAgentWithoutHandlerRestart(t *testing.T) {
	h, ws, crew, actor, _, chat := covQryRig(t)
	post := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"actor_agent_id": actor, "target_slug": "new-peer", "question": "question", "crew_id": crew, "workspace_id": ws, "chat_id": chat})
		rr := httptest.NewRecorder()
		h.Create(rr, withInternalCrewToken(httptest.NewRequest(http.MethodPost, "/internal/queries", strings.NewReader(string(raw))), ws, crew))
		return rr
	}
	if rr := post(); rr.Code != http.StatusNotFound {
		t.Fatalf("uncreated target=%d %s", rr.Code, rr.Body.String())
	}
	id := seedAgentRow(t, h.db, "delegation-new-peer", ws, crew, "New peer", "new-peer", "AGENT")
	// No orchestrator is wired: 503 is the expected execution outcome AFTER
	// live membership admits the request and durably records the exact peer.
	if rr := post(); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("new target=%d %s", rr.Code, rr.Body.String())
	}
	var recorded string
	if err := h.db.QueryRow(`SELECT to_agent_id FROM peer_conversations`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != id {
		t.Fatalf("recorded target=%s want %s", recorded, id)
	}
	execOrFatal(t, h.db, `UPDATE agents SET deleted_at=datetime('now') WHERE id=?`, id)
	if rr := post(); rr.Code != http.StatusNotFound {
		t.Fatalf("deleted target=%d %s", rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM peer_conversations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted target created another dispatch: %d", n)
	}
}

func TestDelegationRefusesSelfBeforeWriting(t *testing.T) {
	t.Run("assign", func(t *testing.T) {
		h, ws, crew, actor, _, chat := covAsgRig(t)
		var slug string
		if err := h.db.QueryRow("SELECT slug FROM agents WHERE id=?", actor).Scan(&slug); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"actor_agent_id": actor, "target_slug": slug, "task": "work", "crew_id": crew, "workspace_id": ws, "chat_id": chat})
		req := withInternalCrewToken(httptest.NewRequest(http.MethodPost, "/internal/assignments", strings.NewReader(string(raw))), ws, crew)
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "yourself") {
			t.Fatalf("self assignment: %d %s", rr.Code, rr.Body.String())
		}
		var rows int
		if err := h.db.QueryRow("SELECT COUNT(*) FROM assignments").Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("assignments=%d err=%v", rows, err)
		}
	})
	t.Run("legacy query", func(t *testing.T) {
		h, ws, crew, _, _, chat := covQryRig(t)
		raw, _ := json.Marshal(map[string]string{"from_slug": "qry2-from", "target_slug": "qry2-from", "question": "question", "crew_id": crew, "workspace_id": ws, "chat_id": chat})
		req := withInternalCrewToken(httptest.NewRequest(http.MethodPost, "/internal/queries", strings.NewReader(string(raw))), ws, crew)
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "yourself") {
			t.Fatalf("self query: %d %s", rr.Code, rr.Body.String())
		}
		var rows int
		if err := h.db.QueryRow("SELECT COUNT(*) FROM peer_conversations").Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("peer conversations=%d err=%v", rows, err)
		}
	})
}
