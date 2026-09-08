package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentKnowledge(t *testing.T) {
	for _, tc := range []struct{ name, file, body, want string }{
		{"normal", "AGENT.md", "remember this", "available"},
		{"empty note", "AGENT.md", "", "available"},
		{"private excluded", "users/person.md", "personal", "empty"},
		{"persona excluded", "PERSONA.md", "instructions", "empty"},
		{"daily", "daily/2026-09-08.md", "today", "available"},
		{"oversized", "AGENT.md", strings.Repeat("a", 65537), "available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "crews", "c", "agents", "a", ".memory")
			target := filepath.Join(dir, filepath.FromSlash(tc.file))
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			docs, state := currentKnowledge(base, dir, "agent", "agent:a/")
			if state != tc.want {
				t.Fatalf("state %s, want %s", state, tc.want)
			}
			if tc.want == "empty" && len(docs) != 0 {
				t.Fatal("excluded data leaked")
			}
			if tc.want == "available" {
				if len(docs) != 1 {
					t.Fatalf("docs %v", docs)
				}
				if len(tc.body) > 65536 {
					if docs[0].Bytes != nil || docs[0].Content != "" || docs[0].State != "unavailable" {
						t.Fatal("oversized document appears readable")
					}
				} else if docs[0].Content != tc.body || docs[0].Revision == "" || docs[0].Bytes == nil {
					t.Fatalf("bad live document: %+v", docs[0])
				}
			}
		})
	}
}
func TestCurrentKnowledgeRejectsSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "private")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "AGENT.md"), []byte("other crew"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	docs, state := currentKnowledge(base, linked, "agent", "")
	if state != "unavailable" || len(docs) != 0 {
		t.Fatal("directory symlink accepted")
	}
	safe := filepath.Join(base, "safe")
	if err := os.Mkdir(safe, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "AGENT.md"), filepath.Join(safe, "AGENT.md")); err != nil {
		t.Fatal(err)
	}
	docs, _ = currentKnowledge(base, safe, "agent", "")
	if len(docs) != 0 {
		t.Fatal("file symlink accepted")
	}
}
func TestMemoryInventoryWorkspaceScope(t *testing.T) {
	rig := peerTestSetup(t)
	h := NewPersonaHandler(rig.db, rig.h.logger, rig.output, nil)
	dir := filepath.Join(rig.output, "crews", rig.crewID, "agents", "alice", ".memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("current without versions"), 0600); err != nil {
		t.Fatal(err)
	}
	req := rig.req(t, "GET", "", map[string]string{"agentId": rig.agentID})
	rec := httptest.NewRecorder()
	h.AgentMemoryInventory(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Documents []memoryDocument `json:"documents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Documents) != 1 || body.Documents[0].Content != "current without versions" {
		t.Fatalf("live file missing: %+v", body)
	}
	req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, "other-workspace"))
	rec = httptest.NewRecorder()
	h.AgentMemoryInventory(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace %d", rec.Code)
	}
}
func TestPeersRejectOtherSubjects(t *testing.T) {
	rig := peerTestSetup(t)
	rig.seedCard(t, "u2", "private")
	for _, method := range []string{"GET", "DELETE"} {
		req := rig.req(t, method, "", map[string]string{"agentId": rig.agentID, "userId": "u2"})
		req = req.WithContext(context.WithValue(req.Context(), ctxRole, "OWNER"))
		rec := httptest.NewRecorder()
		if method == "GET" {
			rig.h.GetAgentPeer(rec, req)
		} else {
			rig.h.DeleteAgentPeer(rec, req)
		}
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s cross-user returned %d", method, rec.Code)
		}
	}
	var count int
	if err := rig.db.QueryRow("SELECT COUNT(*) FROM peer_cards WHERE user_id='u2'").Scan(&count); err != nil || count != 1 {
		t.Fatal("rejected deletion changed storage")
	}
}
