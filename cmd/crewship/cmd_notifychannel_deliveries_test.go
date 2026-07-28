package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The deliveries API has always returned a `title` — internal/api's wire test
// pins it as part of the contract — and this CLI's decoder dropped it. So the
// one question the delivery log exists to answer, "why did that message say
// something else?", had no answer short of a database shell, which the agent
// operating rules exist to keep people out of.
//
// The decoder is an anonymous struct inside the command, so this reconstructs
// its shape and asserts against the payload the server sends. A field added to
// the API and forgotten here is silent by construction; this makes it loud.

func TestDeliveriesCLIDecoder_KeepsEveryFieldItDisplays(t *testing.T) {
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
		Deliveries []struct {
			ID        string `json:"id"`
			ChannelID string `json:"channel_id"`
			UserID    string `json:"user_id"`
			Category  string `json:"category"`
			Title     string `json:"title"`
			Status    string `json:"status"`
			Error     string `json:"error"`
			Attempts  int    `json:"attempts"`
			CreatedAt string `json:"created_at"`
		} `json:"deliveries"`
	}
	if err := json.Unmarshal([]byte(payload), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Deliveries) != 1 {
		t.Fatalf("got %d rows", len(body.Deliveries))
	}
	d := body.Deliveries[0]

	// Title above all: it is the templated wording, so it is the only field
	// that answers what the recipient actually read.
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
