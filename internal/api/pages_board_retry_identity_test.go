package api

import "testing"

func TestBoardInitializationRequiresImmutableTarget(t *testing.T) {
	h, f, boundary := boardHumanFixture(t)
	rejected := boardHumanCall(t, f, boundary, f.userID, "POST", "private-board", `{"mutation_id":"retained-request"}`)
	if rejected.Code != 400 {
		t.Fatalf("slug initialization must require original immutable Page identity: HTTP%d %s", rejected.Code, rejected.Body.String())
	}
	var attached int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_boards`).Scan(&attached); err != nil || attached != 0 {
		t.Fatalf("rejected slug request mutated a Page: count=%d err=%v", attached, err)
	}
}

func TestBoardInitializationRetryNeverFollowsReusedSlug(t *testing.T) {
	h, f, boundary := boardHumanFixture(t)
	body := `{"mutation_id":"retained-request"}`
	initialized := boardHumanCall(t, f, boundary, f.userID, "POST", "@board-human-page", body)
	if initialized.Code != 200 {
		t.Fatalf("original immutable initialization: HTTP%d %s", initialized.Code, initialized.Body.String())
	}
	mustExec(t, h.db, `DELETE FROM pages WHERE id='board-human-page'`)
	f.seedPage(t, "replacement-board-page", "private-board", "Replacement private Board", f.userID, "")
	mustExec(t, h.db, `UPDATE pages SET private_audience=1 WHERE id='replacement-board-page'`)
	retried := boardHumanCall(t, f, boundary, f.userID, "POST", "@board-human-page", body)
	if retried.Code != 404 {
		t.Fatalf("lost-ACK retry followed reused slug: HTTP%d %s", retried.Code, retried.Body.String())
	}
	var attached int
	if err := h.db.QueryRow(`SELECT count(*) FROM page_boards WHERE page_id='replacement-board-page'`).Scan(&attached); err != nil || attached != 0 {
		t.Fatalf("old mutation initialized replacement Page: count=%d err=%v", attached, err)
	}
}
