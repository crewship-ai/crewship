package api

import (
	"net/http"
	"testing"
)

type pageLimitBody struct {
	Max     int `json:"max_pages_per_workspace"`
	Default int `json:"default"`
	Min     int `json:"min"`
	MaxAll  int `json:"max"`
}

const pageLimitPath = "/api/v1/admin/instance/page-limit"

func TestPageLimitAdminRefusesEveryOtherCaller(t *testing.T) {
	f := newInstanceFixture(t)
	seedInstanceUser(t, f.db, "guest", "guest@ex.com", "ws-old", "VIEWER")
	seedInstanceUser(t, f.db, "stranger", "stranger@ex.com", "", "")
	callers := map[string]string{
		"workspace admin":              f.wsAdmin,
		"owner without instance admin": mintTokenFor(t, f.db, "carol", "plcarol00000000000000000"),
		"agent-scoped token":           mintIngressScopedToken(t, f.db, "boss", "plagent000000000000000000", "agents:write"),
		"viewer":                       mintTokenFor(t, f.db, "guest", "plguest0000000000000000000"),
		"non-member":                   mintTokenFor(t, f.db, "stranger", "plstranger000000000000000"),
	}
	for name, tok := range callers {
		for _, c := range []struct{ method, body string }{{"GET", ""}, {"PUT", `{"max_pages_per_workspace":500}`}} {
			wantCode(t, f.do(tok, c.method, pageLimitPath, c.body), http.StatusForbidden, name+" "+c.method)
		}
	}
	var n int
	if err := f.db.QueryRow(`SELECT max_pages_per_workspace FROM instance_page_limits WHERE id=1`).Scan(&n); err != nil || n != 100 {
		t.Fatalf("a refused save still wrote: %d %v", n, err)
	}
}

func TestPageLimitAdminDefaultSetBoundsAndAudit(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "GET", pageLimitPath, "")
	wantCode(t, rr, http.StatusOK, "get")
	got := decodeAs[pageLimitBody](t, rr.Body.Bytes())
	if got.Max != 100 || got.Default != 100 || got.Min != 10 || got.MaxAll != 10000 {
		t.Fatalf("defaults: %+v", got)
	}
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"empty object", `{}`, 400},
		{"below floor", `{"max_pages_per_workspace":9}`, 400},
		{"zero", `{"max_pages_per_workspace":0}`, 400},
		{"negative", `{"max_pages_per_workspace":-5}`, 400},
		{"above ceiling", `{"max_pages_per_workspace":10001}`, 400},
		{"fractional", `{"max_pages_per_workspace":12.5}`, 400},
		{"string", `{"max_pages_per_workspace":"50"}`, 400},
		{"unknown key", `{"max_pages_per_workspace":50,"x":1}`, 400},
		{"not json", `nope`, 400},
		{"lowest", `{"max_pages_per_workspace":10}`, 200},
		{"highest", `{"max_pages_per_workspace":10000}`, 200},
		{"typical", `{"max_pages_per_workspace":250}`, 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, f.do(f.boss, "PUT", pageLimitPath, c.body), c.want, c.name)
		})
	}
	got = decodeAs[pageLimitBody](t, f.do(f.boss, "GET", pageLimitPath, "").Body.Bytes())
	if got.Max != 250 {
		t.Fatalf("last valid value should stick, got %+v", got)
	}
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM instance_audit_logs WHERE action='instance.page_limit_set'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("audit rows %d %v (one per accepted change)", n, err)
	}
	var meta string
	if err := f.db.QueryRow(`SELECT metadata FROM instance_audit_logs WHERE action='instance.page_limit_set' ORDER BY rowid DESC LIMIT 1`).Scan(&meta); err != nil || meta != `{"max_pages_per_workspace_from":10000,"max_pages_per_workspace_to":250}` {
		t.Fatalf("audit metadata %q %v", meta, err)
	}
}
