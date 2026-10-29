package api

import (
	"net/http"
	"testing"
)

func TestFolderDeleteAuditFinalFrames(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "later_tamper"}[tamper], func(t *testing.T) {
			f := newFoldersFixture(t)
			f.createFolder(t, "final-frames", "Final frames", "crew/engine")
			f.share(t, "final-frames", "user", "frank", true)
			f.share(t, "final-frames", "user", "dave", false)
			execOrFatal(t, f.h.db, `CREATE TABLE folder_delete_tamper_hits(hit INTEGER NOT NULL)`)
			if tamper {
				// The current lifecycle row remains intact. Only a previously
				// acknowledged ACL event from this very deletion is changed.
				execOrFatal(t, f.h.db, `CREATE TRIGGER late_folder_delete_tamper AFTER INSERT ON journal_entries
WHEN NEW.entry_type='page.folder_changed' AND json_extract(NEW.payload,'$.op')='deleted'
AND json_extract(NEW.payload,'$.folder')='final-frames'
BEGIN
 UPDATE journal_entries SET summary='synthetic earlier audit tamper' WHERE id=(
  SELECT id FROM journal_entries WHERE workspace_id=NEW.workspace_id
  AND entry_type='page.folder_acl_changed' AND json_extract(payload,'$.op')='removed'
  AND json_extract(payload,'$.folder')='final-frames' ORDER BY seq LIMIT 1);
 INSERT INTO folder_delete_tamper_hits(hit) VALUES(changes());
END`)
			}
			beforeNotify := len(f.spy.entries)
			rr := f.call(t, "DELETE", "/api/v1/page-folders/final-frames", "erin", "ADMIN", "")
			status, folders, entries, events, notifications := http.StatusNoContent, 0, 0, 3, 3
			if tamper {
				status, folders, entries, events, notifications = http.StatusInternalServerError, 1, 2, 0, 0
			}
			if rr.Code != status {
				t.Errorf("folder deletion HTTP%d want%d", rr.Code, status)
			}
			for _, check := range []struct {
				query string
				want  int
			}{
				{`SELECT count(*) FROM page_folders WHERE slug='final-frames'`, folders},
				{`SELECT count(*) FROM page_folder_acl WHERE folder_id IN (SELECT id FROM page_folders WHERE slug='final-frames')`, entries},
				{`SELECT count(*) FROM journal_entries WHERE json_extract(payload,'$.folder')='final-frames' AND (entry_type='page.folder_acl_changed' AND json_extract(payload,'$.op')='removed' OR entry_type='page.folder_changed' AND json_extract(payload,'$.op')='deleted')`, events},
				{`SELECT count(*) FROM folder_delete_tamper_hits`, 0},
				{`SELECT COALESCE(sum(hit),0) FROM folder_delete_tamper_hits`, 0},
			} {
				var got int
				if err := f.h.db.QueryRow(check.query).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != check.want {
					t.Errorf("native postcondition count=%d want%d: %s", got, check.want, check.query)
				}
			}
			if got := len(f.spy.entries) - beforeNotify; got != notifications {
				t.Errorf("postcommit notifications=%d want%d", got, notifications)
			}
		})
	}
}
