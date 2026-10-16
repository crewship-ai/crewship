package api

import "testing"

func TestPageContentPresenceLiveACLAndType(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Private</h1>", "source", 0), 201)
	pagesSeedUser(t, h, ws, "presence-reader", "presence-reader@example.com", "MEMBER")
	check := func(who, kind, id string, want bool) {
		t.Helper()
		got, err := h.CanReadPageContent(t.Context(), who, kind, id)
		if err != nil || got != want {
			t.Fatalf("%s %s %s: %v %v", who, kind, id, got, err)
		}
	}
	check(user, "artifact", page, true)
	check(user, "board", page, false)
	check("presence-reader", "artifact", page, false)
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"presence-reader@example.com","level":"read"}`)
	check("presence-reader", "artifact", page, true)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_id='presence-reader'`)
	check("presence-reader", "artifact", page, false)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('presence-foreign','Foreign','presence-foreign')`)
	pagesSeedUser(t, h, "presence-foreign", "foreign-user", "foreign-presence@example.com", "OWNER")
	check("foreign-user", "artifact", page, false)
	seedStorageBoardPin(t, h, ws, user, page, 1)
	pagesGrant(t, h, ws, user, "storage-board", `{"subject_type":"user","subject":"presence-reader@example.com","level":"read"}`)
	check("presence-reader", "board", "storage-board", true)
	execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id='presence-reader'`)
	check("presence-reader", "board", "storage-board", false)
	execOrFatal(t, h.db, `UPDATE users SET suspended_at=NULL WHERE id='presence-reader'`)
	check("presence-reader", "board", "storage-board", true)
	execOrFatal(t, h.db, `UPDATE workspaces SET deleted_at='2026-10-13T00:00:00Z' WHERE id=?`, ws)
	check("presence-reader", "board", "storage-board", false)
	execOrFatal(t, h.db, `UPDATE workspaces SET deleted_at=NULL WHERE id=?`, ws)
	check("presence-reader", "board", "storage-board", true)
	check("presence-reader", "artifact", page, false)
	pagesGrant(t, h, ws, user, "report", `{"subject_type":"user","subject":"presence-reader@example.com","level":"read"}`)
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE user_id='presence-reader'`)
	check("presence-reader", "artifact", page, false)
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, page)
	check(user, "artifact", page, false)
}
