package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func TestMooringManifestCommandDurabilityAndAuthority(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	f.r.RegisterGridCraft(appengine.Options{Enabled: true}, gridcraftFixtureEngine{})
	apps := f.do(f.boss, "GET", "/api/v1/mooring/apps?workspace_id=ws-old", "")
	wantCode(t, apps, 200, "manifest discovery")
	if !strings.Contains(apps.Body.String(), `"input_schema"`) {
		t.Fatal(apps.Body.String())
	}
	body := `{"app_id":"hello","slug":"generic-hello","name":"Generic document"}`
	created := f.do(f.boss, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", body)
	wantCode(t, created, 201, "create")
	var page struct {
		ID string `json:"page_id"`
	}
	if json.Unmarshal(created.Body.Bytes(), &page) != nil || page.ID == "" {
		t.Fatal(created.Body.String())
	}
	base := "/api/v1/pages/generic-hello/mooring"
	wantCode(t, f.do(f.boss, "POST", "/api/v1/mooring/documents?workspace_id=ws-old", body), 200, "create retry")
	wantCode(t, f.do(f.wsAdmin, "GET", base+"?workspace_id=ws-old", ""), 404, "private content no implicit admin")
	packet := `{"mutation_id":"one","base_rev":1,"generation":1,"command":"set","arguments":{"text":"Durable český text"}}`
	for i := 0; i < 2; i++ {
		w := f.do(f.boss, "POST", base+"/commands?workspace_id=ws-old", packet)
		wantCode(t, w, 200, "commit retry")
		var receipt struct {
			Rev        int    `json:"rev"`
			Outcome    string `json:"outcome"`
			Durability string `json:"durability"`
		}
		if json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Rev != 2 || receipt.Outcome != "committed" || receipt.Durability != "node" {
			t.Fatal(w.Body.String())
		}
	}
	wantCode(t, f.do(f.boss, "POST", base+"/commands?workspace_id=ws-old", strings.Replace(packet, "Durable český text", "different", 1)), 409, "mutation collision")
	wantCode(t, f.do(f.boss, "POST", base+"/commands?workspace_id=ws-old", strings.Replace(packet, `"one"`, `"two"`, 1)), 409, "stale full replacement refuses draft overwrite")
	for _, bad := range []string{`{"text":42}`, `{"text":"ok","identity":"owner"}`, `{}`} {
		req := fmt.Sprintf(`{"mutation_id":"invalid","base_rev":2,"generation":1,"command":"set","arguments":%s}`, bad)
		wantCode(t, f.do(f.boss, "POST", base+"/commands?workspace_id=ws-old", req), 400, "schema refusal")
	}
	read := f.do(f.boss, "GET", base+"?workspace_id=ws-old", "")
	wantCode(t, read, 200, "fresh HTTP read")
	if !strings.Contains(read.Body.String(), "Durable český text") || !strings.Contains(read.Body.String(), `"rev":2`) {
		t.Fatal(read.Body.String())
	}
	var n int
	if e := f.db.QueryRow(`SELECT COUNT(*) FROM mooring_receipts WHERE page_id=?`, page.ID).Scan(&n); e != nil || n != 1 {
		t.Fatalf("receipts=%d: %v", n, e)
	}
	// Existing format initialization cannot silently attach another truth store.
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages/generic-hello/workbook/operations?workspace_id=ws-old", `{"mutation_id":"import","mandate_id":"human-import","base_rev":0,"generation":0,"command":{"kind":"import","snapshot":{"sheets":[{}]}}}`), 404, "another format refused")
	mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
	wantCode(t, f.do(f.boss, "POST", base+"/commands?workspace_id=ws-old", packet), 401, "revocation stops exact replay")
}
