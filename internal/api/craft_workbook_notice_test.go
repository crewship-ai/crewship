package api

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// With workbook Sheets on, the server names how many Sheets are still native at start and how to
// migrate them; it changes nothing. A native server and a server with none waiting say nothing.
func TestWorkbookPendingNoticeAtStart(t *testing.T) {
	rig := workbookMigrationFixture(t, "wb-a", "wb-b")
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, nil))
	craftWorkbookPendingNotice(t.Context(), rig.f.db, rig.d.applications, logger)
	if !strings.Contains(out.String(), "pending=2") || !strings.Contains(out.String(), "migrate-workbooks --dry-run") {
		t.Fatalf("notice: %s", out.String())
	}
	if v, entry := rig.head(t, "wb-a"); v != 1 || entry != "document.json" {
		t.Fatalf("the notice wrote: %d %s", v, entry)
	}
	out.Reset()
	native := map[string]craftNativeApplication{"gridcraft": {Store: rig.d.applications["gridcraft"].Store}}
	p := native["gridcraft"]
	p.Store.Format = ""
	native["gridcraft"] = p
	craftWorkbookPendingNotice(t.Context(), rig.f.db, native, logger)
	if out.Len() != 0 {
		t.Fatalf("a native server warned: %s", out.String())
	}
}
