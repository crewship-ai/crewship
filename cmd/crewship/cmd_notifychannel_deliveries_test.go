package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// The deliveries API has always returned a `title` — internal/api's wire test
// pins it as part of the contract — and this CLI dropped it. So the one
// question the delivery log exists to answer, "why did that message say
// something else?", had no answer short of a database shell, which the agent
// operating rules exist to keep people out of.
//
// It was silent because the decoder was an anonymous struct inside the
// command: a field the API adds and this file forgets costs nothing at
// compile time. These tests hold the REAL type and the REAL renderer, so a
// field dropped from either fails here rather than disappearing.

func TestDeliveriesCLI_DecodesEveryFieldTheAPISends(t *testing.T) {
	// The server's shape, as pinned by
	// internal/api/notification_deliveries_wire_test.go.
	const payload = `{"deliveries":[{
		"id":"del_1","workspace_id":"ws1","channel_id":"nch_1","user_id":"u1",
		"category":"routines.completed","dedup_key":"k","source_kind":"journal:x",
		"source_id":"je_1","title":"✅ nightly is done","status":"sent",
		"attempts":1,"created_at":"2026-07-28T13:00:00.000Z",
		"updated_at":"2026-07-28T13:00:00.000Z"
	}]}`

	var body struct {
		Deliveries []notifyDeliveryRow `json:"deliveries"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Deliveries) != 1 {
		t.Fatalf("got %d rows", len(body.Deliveries))
	}
	d := body.Deliveries[0]

	// Title above all: it carries the message template, so it is the only
	// field that says what the recipient actually read.
	if d.Title != "✅ nightly is done" {
		t.Errorf("title = %q — the CLI must surface what was sent", d.Title)
	}
	for name, got := range map[string]string{
		"id": d.ID, "channel_id": d.ChannelID, "user_id": d.UserID,
		"category": d.Category, "status": d.Status, "created_at": d.CreatedAt,
	} {
		if strings.TrimSpace(got) == "" {
			t.Errorf("%s decoded empty", name)
		}
	}
}

func TestDeliveriesCLI_RendersTheTitleItDecoded(t *testing.T) {
	// Decoding a field and never printing it would be the same bug wearing
	// different clothes, so the renderer is checked against the header rather
	// than trusted to agree with it.
	row := notifyDeliveryRow{
		ID: "del_1", ChannelID: "nch_1", UserID: "u1",
		Category: "routines.completed", Title: "✅ nightly is done",
		Status: "sent", Attempts: 1, CreatedAt: "2026-07-28T13:00:00.000Z",
	}
	cells := notifyDeliveryCells(cli.NewFormatter("table"), row)

	if len(cells) != len(notifyDeliveryColumns) {
		t.Fatalf("%d cells for %d columns — the header and the row have drifted",
			len(cells), len(notifyDeliveryColumns))
	}
	i := slices.Index(notifyDeliveryColumns, "TITLE")
	if i < 0 {
		t.Fatal("the rendered table has no TITLE column")
	}
	if !strings.Contains(cells[i], "nightly is done") {
		t.Errorf("the TITLE column shows %q, not the delivered wording", cells[i])
	}
}

func TestDeliveriesCLI_TruncatesALongTitleRatherThanBreakingTheTable(t *testing.T) {
	row := notifyDeliveryRow{Title: strings.Repeat("x", 200)}
	cells := notifyDeliveryCells(cli.NewFormatter("table"), row)
	i := slices.Index(notifyDeliveryColumns, "TITLE")
	if len([]rune(cells[i])) > 40 {
		t.Errorf("TITLE cell is %d runes; a templated title can be long and must not "+
			"push every other column off the terminal", len([]rune(cells[i])))
	}
}
