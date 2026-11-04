package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestJournalHandlerListMalformedCursorIsClientError(t *testing.T) {
	h, user, workspace, _ := newJournalHandlerTest(t)
	for _, cursor := range []string{"}", "no-separator-here"} {
		t.Run(cursor, func(t *testing.T) {
			req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/journal?cursor="+url.QueryEscape(cursor), nil), user, workspace, "OWNER")
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "cursor") {
				t.Fatalf("malformed cursor: status=%d body=%s", rr.Code, rr.Body.String())
			}
			if rr.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected error envelope: %s", rr.Header().Get("Content-Type"))
			}
		})
	}
}

func TestJournalHandlerListStorageFailureRemainsServerError(t *testing.T) {
	h, user, workspace, _ := newJournalHandlerTest(t)
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/journal", nil), user, workspace, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "journal list failed") {
		t.Fatalf("storage failure: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestJournalHandlerListServerCursorRoundTrip(t *testing.T) {
	h, user, workspace, _ := newJournalHandlerTest(t)
	now := time.Now().UTC()
	seedJournalRow(t, h, "j_old", workspace, "run.started", "info", "old", now.Add(-time.Second))
	seedJournalRow(t, h, "j_new", workspace, "run.completed", "info", "new", now)
	cursor := ""
	for page, want := range []string{"j_new", "j_old", ""} {
		req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/journal?limit=1&cursor="+url.QueryEscape(cursor), nil), user, workspace, "OWNER")
		rr := httptest.NewRecorder()
		h.List(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("page%d status=%d body=%s", page, rr.Code, rr.Body.String())
		}
		var body struct {
			Entries []struct {
				ID string `json:"id"`
			} `json:"entries"`
			Next string `json:"next_cursor"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(body.Entries) != 0 || body.Next != "" {
				t.Fatalf("pagination did not end: %+v", body)
			}
			continue
		}
		if len(body.Entries) != 1 || body.Entries[0].ID != want || body.Next == "" || body.Next == cursor {
			t.Fatalf("page%d: %+v", page, body)
		}
		cursor = body.Next
	}
}
