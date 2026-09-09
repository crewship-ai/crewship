package api

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

func TestReencrypt_PrivateServicesSurviveOldKeyRemoval(t *testing.T) {
	reencSetV1(t)
	db := setupTestDB(t)
	const raw = `[{"name":"db","image":"postgres:16","env":{"POSTGRES_PASSWORD":"inert-rotation-canary"}}]`
	sealed, err := serviceconfig.Seal(raw)
	if err != nil {
		t.Fatal(err)
	}
	reencExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('ws1','W','w1')`)
	reencExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug,services_json) VALUES('private','ws1','Private','private',?)`, sealed)
	reencExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug,services_json) VALUES('public','ws1','Public','public','[]')`)
	reencRotateToV2(t)
	rec, out := callReencrypt(t, db, "OWNER")
	if rec.Code != 200 || out.Reencrypted != 1 || out.Failed != 0 {
		t.Fatalf("rotation status=%d rotated=%d failed=%d", rec.Code, out.Reencrypted, out.Failed)
	}
	t.Setenv("ENCRYPTION_KEY", "")
	var stored string
	if err := db.QueryRow(`SELECT services_json FROM crews WHERE id='private'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "crewsvc:v2:") {
		t.Fatal("service envelope did not rotate")
	}
	plain, err := serviceconfig.Open(stored)
	if err != nil || plain != raw {
		t.Fatal("service unavailable after old key removal")
	}
	if err := db.QueryRow(`SELECT services_json FROM crews WHERE id='public'`).Scan(&stored); err != nil || stored != "[]" {
		t.Fatal("public topology changed")
	}
	rec, out = callReencrypt(t, db, "OWNER")
	if rec.Code != 200 || out.Reencrypted != 0 || out.Failed != 0 {
		t.Fatal("rotation not idempotent")
	}
}
