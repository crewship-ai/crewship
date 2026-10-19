package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestCredentialTombstoneRecreationPreservesAudit(t *testing.T) {
	h, db := newCredHandler(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	seedCredentialEnc(t, db, ws, user, "retired-credential", "TOKEN", "old-secret")
	execOrFatal(t, db, `INSERT INTO credential_audit(id,credential_id,workspace_id,event_type,metadata_json) VALUES('retained-history','retired-credential',?,'READ','{"proof":"unchanged"}')`, ws)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("credentialId", "retired-credential")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
	out := httptest.NewRecorder()
	h.Delete(out, req)
	if out.Code != 200 {
		t.Fatalf("delete: %d %s", out.Code, out.Body.String())
	}
	create := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"TOKEN","value":"new-secret"}`))
	create = create.WithContext(withWorkspace(withUser(create.Context(), &AuthUser{ID: user}), ws, "OWNER"))
	out = httptest.NewRecorder()
	h.Create(out, create)
	if out.Code != 201 {
		t.Fatalf("recreate: %d %s", out.Code, out.Body.String())
	}
	execOrFatal(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('retired-crew',?,'Retired','retired')`, ws)
	execOrFatal(t, db, `INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('retired-agent',?,'retired-crew','Retired','retired')`, ws)
	// A surviving historical assignment must not grant the replacement ID.
	execOrFatal(t, db, `INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('old-assignment','retired-agent','retired-credential','TOKEN')`)
	delivered, _, err := loadDeliveredCredentials(context.Background(), db, "retired-agent")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range delivered {
		if c.ID == "retired-credential" {
			t.Fatal("retired ID was delivered")
		}
	}
	value, err := pipeline.NewVaultCredentialResolver(db)(context.Background(), pipeline.RunScope{WorkspaceID: ws}, "SECRET")
	if err != nil || value != "new-secret" {
		t.Fatalf("replacement unusable: %q %v", value, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE id='retained-history' AND credential_id='retired-credential' AND metadata_json='{"proof":"unchanged"}'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("same-name recreation erased credential audit history")
	}
	var retired int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE id='retired-credential' AND deleted_at IS NOT NULL`).Scan(&retired); err != nil || retired != 1 {
		t.Fatalf("missing tombstone: %d %v", retired, err)
	}
	get := httptest.NewRequest("GET", "/", nil)
	get.SetPathValue("credentialId", "retired-credential")
	get = get.WithContext(withWorkspace(withUser(get.Context(), &AuthUser{ID: user}), ws, "OWNER"))
	out = httptest.NewRecorder()
	h.Get(out, get)
	if out.Code != 404 {
		t.Fatalf("retired credential readable: %d", out.Code)
	}
}

func TestCredentialTombstoneReplacementRollback(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "insert-failure", true: "name-collision"}[collision], func(t *testing.T) {
			h, db := newCredHandler(t)
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			seedCredentialEnc(t, db, ws, user, "retired", "TOKEN", "cipher-source")
			var beforeCipher string
			if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id='retired'`).Scan(&beforeCipher); err != nil {
				t.Fatal(err)
			}
			execOrFatal(t, db, `UPDATE credentials SET deleted_at='2026-01-01' WHERE id='retired'`)
			if collision {
				seedCredentialEnc(t, db, ws, user, "collision", "TOKEN [retired:retired]", "other")
			} else {
				execOrFatal(t, db, `CREATE TRIGGER reject_replacement BEFORE INSERT ON credentials BEGIN SELECT RAISE(ABORT,'replacement failed'); END`)
			}
			req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"TOKEN","value":"replacement"}`))
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER"))
			out := httptest.NewRecorder()
			h.Create(out, req)
			if out.Code < 400 {
				t.Fatalf("failed replacement accepted: %d", out.Code)
			}
			var name, cipher string
			if err := db.QueryRow(`SELECT name,encrypted_value FROM credentials WHERE id='retired' AND deleted_at IS NOT NULL`).Scan(&name, &cipher); err != nil {
				t.Fatal(err)
			}
			if name != "TOKEN" {
				t.Fatalf("failed replacement changed original name: %s", name)
			}
			if cipher != beforeCipher {
				t.Fatal("tombstone ciphertext changed")
			}
		})
	}
}
