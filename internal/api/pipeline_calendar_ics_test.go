package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// The Calendar's "Subscribe (.ics)" button needs a feed calendar apps can
// read. RoutineCalendarICS answers the same window as the JSON calendar as
// an iCalendar file.
func TestRoutineCalendarICS(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	execOrFatal(t, db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('p-ics', ?, 'weekly-scan', 'Weekly scan, EU; "prices"', '{"name":"x","steps":[]}', 'h')`, ws)
	fire := time.Date(2030, 3, 4, 7, 30, 0, 0, time.UTC)
	if _, _, err := pipeline.NewPendingRunStore(db).Enqueue(context.Background(), pipeline.PendingRun{
		ID: "pend-ics", WorkspaceID: ws, PipelineID: "p-ics", PipelineSlug: "weekly-scan", FireAt: fire,
	}); err != nil {
		t.Fatal(err)
	}
	get := func(role, query string) *httptest.ResponseRecorder {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/calendar.ics"+query, nil), user, ws, role)
		rr := httptest.NewRecorder()
		h.RoutineCalendarICS(rr, req)
		return rr
	}
	window := "?from=2030-03-01T00:00:00Z&to=2030-03-10T00:00:00Z"

	t.Run("a window with a start becomes a VEVENT", func(t *testing.T) {
		rr := get("VIEWER", window)
		if rr.Code != 200 {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/calendar") {
			t.Errorf("content type = %q", ct)
		}
		if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "crewship-calendar.ics") {
			t.Errorf("content disposition = %q", cd)
		}
		body := rr.Body.String()
		for _, want := range []string{
			"BEGIN:VCALENDAR\r\n", "VERSION:2.0\r\n", "PRODID:-//Crewship//Routine calendar//EN\r\n",
			"BEGIN:VEVENT\r\n", "UID:pend-ics@crewship\r\n", "DTSTART:20300304T073000Z\r\n",
			"SUMMARY:Weekly scan\\, EU\\; \"prices\"\r\n", "END:VEVENT\r\n", "END:VCALENDAR\r\n",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q in:\n%s", want, body)
			}
		}
		for _, line := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
			if len(line) > 75 {
				t.Errorf("line longer than 75 octets: %q", line)
			}
		}
		if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") {
			t.Error("bare LF in output; iCalendar lines end in CRLF")
		}
	})

	t.Run("an empty window is still a valid calendar", func(t *testing.T) {
		rr := get("VIEWER", "?from=2031-01-01T00:00:00Z&to=2031-01-05T00:00:00Z")
		if rr.Code != 200 || strings.Contains(rr.Body.String(), "BEGIN:VEVENT") || !strings.Contains(rr.Body.String(), "END:VCALENDAR") {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("bad windows are 400", func(t *testing.T) {
		for _, q := range []string{"", "?from=yesterday&to=x", "?from=2030-01-01T00:00:00Z&to=2030-03-01T00:00:00Z"} {
			if rr := get("VIEWER", q); rr.Code != 400 {
				t.Errorf("%q -> %d", q, rr.Code)
			}
		}
	})

	t.Run("a long name folds", func(t *testing.T) {
		long := strings.Repeat("Quarterly competitor review ", 6)
		execOrFatal(t, db, `UPDATE pipelines SET name = ? WHERE id = 'p-ics'`, long)
		body := get("VIEWER", window).Body.String()
		if !strings.Contains(body, "\r\n ") {
			t.Errorf("expected a folded continuation line:\n%s", body)
		}
		unfolded := strings.ReplaceAll(body, "\r\n ", "")
		if !strings.Contains(unfolded, "SUMMARY:"+strings.TrimSpace(long)) && !strings.Contains(unfolded, "SUMMARY:"+long) {
			t.Errorf("unfolding does not restore the name:\n%s", unfolded)
		}
	})
}
