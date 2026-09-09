package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func testApplicationHistory(t *testing.T, h *PageHandler, ws, user string, version int64) {
	t.Helper()
	var panel, row string
	if err := h.db.QueryRow(`SELECT panel_id,id FROM page_panels WHERE page_id=(SELECT id FROM pages WHERE workspace_id=? AND slug='health') LIMIT 1`, ws).Scan(&panel, &row); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 23; i++ {
		if _, err := h.db.Exec(`INSERT INTO page_panel_data(panel_id,seq,payload_json,produced_at,state) VALUES(?,?,?,'2026-09-09','ok')`, row, i, fmt.Sprintf(`{"value":%d}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	call := func(workspace, actor, query string) *httptest.ResponseRecorder {
		role := "MEMBER"
		if actor == user {
			role = "OWNER"
		}
		req := pagesRequest(t, "GET", "/?"+query, workspace, actor, role, "")
		req.SetPathValue("slug", "health")
		req.SetPathValue("panelId", panel)
		w := httptest.NewRecorder()
		h.ApplicationPanelHistory(w, req)
		return w
	}
	query := fmt.Sprintf("publication=%d", version)
	w := call(ws, user, query)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result struct {
		Items []struct {
			Sequence int64 `json:"sequence"`
		}
		Next int64 `json:"next_before"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 20 || result.Items[0].Sequence != 23 || result.Next != 4 {
		t.Fatalf("bad bounded history: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "run_id") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("history exposes metadata or caching")
	}
	w = call(ws, user, query+"&before=4&limit=2")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Items) != 2 || result.Next != 2 {
		t.Fatalf("bad cursor: %s", w.Body.String())
	}
	escaped := []byte(`{"text":"` + strings.Repeat("<", 220000) + `"}`)
	if _, err := h.db.Exec(`UPDATE page_panel_data SET payload_json=? WHERE panel_id=? AND seq=23`, string(escaped), row); err != nil {
		t.Fatal(err)
	}
	if w := call(ws, user, query); w.Code != 413 || w.Body.Len() > 1024*1024 {
		t.Fatalf("escaped payload bypassed response bound: %d bytes=%d", w.Code, w.Body.Len())
	}
	if _, err := h.db.Exec(`UPDATE page_panel_data SET payload_json='{}' WHERE panel_id=? AND seq=23`, row); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		workspace, actor, query string
		status                  int
	}{
		{ws, user, query + "&limit=21", 400}, {ws, user, query + "&before=-1", 400}, {ws, user, "publication=999", 409}, {"foreign", user, query, 404}, {ws, "unauthorized-viewer", query, 404},
	} {
		if w := call(test.workspace, test.actor, test.query); w.Code != test.status {
			t.Fatalf("history gate %s: %d %s", test.query, w.Code, w.Body.String())
		}
	}
	if _, err := h.db.Exec(`UPDATE page_project_live SET published=0`); err != nil {
		t.Fatal(err)
	}
	if w := call(ws, user, query); w.Code != 409 {
		t.Fatal("withdrawn application read history", w.Code)
	}
	if _, err := h.db.Exec(`UPDATE page_project_live SET published=1`); err != nil {
		t.Fatal(err)
	}
}
