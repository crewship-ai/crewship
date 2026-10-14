package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Flush ends the actual SSE seed after an entry arrives, without relying on a
// synthetic serializer or leaving a streaming handler running after the test.
type custodyJournalStreamRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *custodyJournalStreamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	if strings.Contains(w.Body.String(), "event: entry") {
		w.cancel()
	}
}

func TestOutputCustodyJournalDoesNotExposePrivateMetadata(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := custodyRouterFixture(t, kind)
			mustExec(t, f.db, `UPDATE pages SET slug='secret-custody-slug',name='Secret custody title' WHERE id='private-output'`)
			writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("custody-test-journal-chain-key-32"), FlushSize: 1})
			t.Cleanup(func() {
				if err := writer.Close(); err != nil {
					t.Errorf("close journal: %v", err)
				}
			})
			var err error
			f.r, err = NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithJournal(writer))
			if err != nil {
				t.Fatal(err)
			}
			member := mintTokenFor(t, f.db, "ungranted", "custodyjournalmember000000000000")
			wantCode(t, f.do(member, "GET", "/api/v1/pages/secret-custody-slug?workspace_id=ws-old", ""), 404, "ungranted member cannot open output")
			wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/workspaces/ws-old/page-ownership/private-output", `{"expected_owner_user_id":"departing","successor_user_id":"reader"}`), 200, "explicit transfer")
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			var id string
			if err := f.db.QueryRow(`SELECT id FROM journal_entries WHERE workspace_id='ws-old' AND entry_type='page.owner_transferred'`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			check := func(label, body string) {
				t.Helper()
				for _, private := range []string{"secret-custody-slug", "Secret custody title", "page_slug", "page_name", "NEVER_EXPOSE_THIS"} {
					if strings.Contains(body, private) {
						t.Errorf("%s leaked private metadata %q", label, private)
					}
				}
				for _, opaque := range []string{"private-output", "explicit_human_transfer", "departing", "reader"} {
					if !strings.Contains(body, opaque) {
						t.Errorf("%s lost opaque audit field %q", label, opaque)
					}
				}
			}
			list := f.do(member, "GET", "/api/v1/journal?workspace_id=ws-old&entry_type=page.owner_transferred", "")
			wantCode(t, list, 200, "member journal list")
			check("list", list.Body.String())
			get := f.do(member, "GET", "/api/v1/journal/"+id+"?workspace_id=ws-old", "")
			wantCode(t, get, 200, "member journal get")
			check("get", get.Body.String())
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			request := httptest.NewRequest("GET", "/api/v1/journal/stream?workspace_id=ws-old&entry_type=page.owner_transferred", nil).WithContext(ctx)
			request.Header.Set("Authorization", "Bearer "+member)
			stream := &custodyJournalStreamRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			f.r.ServeHTTP(stream, request)
			wantCode(t, stream.ResponseRecorder, 200, "member journal stream")
			if !strings.Contains(stream.Body.String(), "event: entry") {
				t.Fatal("real SSE seed did not deliver the transfer")
			}
			check("stream", stream.Body.String())
		})
	}
}
