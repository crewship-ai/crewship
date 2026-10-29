package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Each commit publishes the compatibility and generic revision notices on the
// authorized Page channel, without cell content. The sweeper delivers pending rows.
func TestGridCraftCommittedPushOnPageChannel(t *testing.T) {
	f := gridcraftInstanceFixture(t)
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go hub.Run(ctx)
	r, err := NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Shutdown)
	f.r = r
	f.r.RegisterGridCraft(appengine.Options{Enabled: true, Editor: true}, gridcraftCellsEngine{})
	setGridCraftEditorFlag(t, f, "ws-old", true)
	observer := hub.AddObserver("page:push-book", "boss", 16)
	t.Cleanup(func() { hub.RemoveObserver("page:push-book", observer) })
	nextFrame := func(name, kind string) map[string]any {
		t.Helper()
		select {
		case raw := <-observer.Frames():
			if strings.Contains(string(raw), "secret-input") || strings.Contains(string(raw), `"B2"`) {
				t.Fatalf("%s: push carries cell content: %s", name, raw)
			}
			var frame struct {
				Type    string         `json:"type"`
				Channel string         `json:"channel"`
				Payload map[string]any `json:"payload"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != kind || frame.Channel != "page:push-book" {
				t.Fatalf("%s: frame %s", name, raw)
			}
			return frame.Payload
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no push", name)
		}
		return nil
	}
	next := func(name string) map[string]any {
		t.Helper()
		legacy := nextFrame(name, "workbook.committed")
		generic := nextFrame(name, "mooring.committed")
		want := map[string]any{"artifact_id": "push-book", "rev": legacy["rev"], "generation": legacy["generation"]}
		if fmt.Sprint(generic) != fmt.Sprint(want) {
			t.Fatalf("%s: generic push %v, want %v", name, generic, want)
		}
		return legacy
	}
	base := liveWorkbook(t, f, f.boss, "boss", "push-book")
	if p := next("import"); fmt.Sprint(p) != fmt.Sprint(map[string]any{"page_id": "push-book", "rev": float64(1), "generation": float64(1), "kind": "snapshot", "cells": float64(0)}) {
		t.Fatalf("import push %v", p)
	}
	s := openEditorSession(t, f, f.boss, base)
	body := fmt.Sprintf(`{"session_id":%q,"mutation_id":"%s:1","base_rev":1,"generation":1,"ops":[{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"secret-input"},{"kind":"cell.set","sheet":"Sheet1","cell":"B3","input":"secret-input"}]}`, s.SessionID, s.SessionID)
	wantCode(t, f.do(f.boss, "POST", base+"/edits?workspace_id=ws-old", body), 200, "edit")
	if p := next("edit"); p["rev"] != float64(2) || p["kind"] != "cells" || p["cells"] != float64(2) || len(p) != 5 {
		t.Fatalf("edit push %v", p)
	}
	var pending int
	if err := f.db.QueryRow(`SELECT count(*) FROM gridcraft_outbox WHERE state='pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending=%d %v", pending, err)
	}
	// A crash between commit and push leaves the row pending; the sweeper
	// delivers it.
	mustExec(t, f.db, `UPDATE gridcraft_outbox SET state='pending' WHERE page_id='push-book' AND rev=2`)
	f.r.gridcraftOutbox.sweep(t.Context())
	if p := next("sweep"); p["rev"] != float64(2) {
		t.Fatalf("swept push %v", p)
	}
	if err := f.db.QueryRow(`SELECT count(*) FROM gridcraft_outbox WHERE state='pending'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending after sweep=%d %v", pending, err)
	}
	status := f.do(f.boss, "GET", base+"/operations/"+s.SessionID+":1?workspace_id=ws-old", "")
	var st struct {
		Delivery string `json:"delivery"`
	}
	if json.Unmarshal(status.Body.Bytes(), &st) != nil || st.Delivery != "sent" {
		t.Fatalf("status %s", status.Body.String())
	}
}
