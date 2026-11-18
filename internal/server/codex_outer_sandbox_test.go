package server

import (
	"context"
	"testing"
)

func TestCodexSandboxConsentIsDefaultOffAndTenantScoped(t *testing.T) {
	db, close := newTestDB(t)
	defer close()
	for _, query := range []string{`ALTER TABLE crews ADD COLUMN deleted_at TEXT`, `ALTER TABLE crews ADD COLUMN allow_codex_outer_sandbox INTEGER NOT NULL DEFAULT 0`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	resolve := codexSandboxPolicyResolver(db)
	p, err := resolve(context.Background(), "ws-1", "crew-1")
	if err != nil || p.Enabled || p.Launch != nil {
		t.Fatalf("default policy=%+v %v", p, err)
	}
	for _, scope := range [][2]string{{"", "crew-1"}, {"ws-1", ""}, {"other", "crew-1"}} {
		if p, err := resolve(context.Background(), scope[0], scope[1]); err == nil || p.Enabled {
			t.Fatal("unscoped or cross-tenant consent accepted")
		}
	}
	if _, err := db.Exec(`UPDATE crews SET allow_codex_outer_sandbox=1 WHERE id='crew-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(context.Background(), "ws-1", "crew-1"); err == nil {
		t.Fatal("consent alone bypassed missing immutable evidence")
	}
}
