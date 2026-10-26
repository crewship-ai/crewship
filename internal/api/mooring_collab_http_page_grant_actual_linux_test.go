//go:build linux && mooring_rust_integration

package api

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"golang.org/x/net/websocket"
)

// The tokens are synthetic signed JWT fixtures; transport, native preparation,
// Page grant administration, source admission and Saved outcomes are real.
func TestMooringHTTPActualPageGrantRevocation(t *testing.T) {
	for _, mode := range []string{"revoke", "reader", "late-ticket"} {
		t.Run(mode, func(t *testing.T) { runMooringHTTPActualSDK(t, false, mode) })
	}
}

type mooringPageGrantProbe struct {
	db            *sql.DB
	owner, bob    *websocket.Conn
	submitBob     func(int) error
	changeGrant   func(string)
	ownerContinue func()
	oldTicket     func() error
	newReader     func()
}

func runMooringPageGrantTransition(t *testing.T, mode string, p mooringPageGrantProbe) {
	t.Helper()
	receive := func(ws *websocket.Conn) []byte {
		t.Helper()
		_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
		var raw []byte
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			t.Fatal("actual participant receive", err)
		}
		return raw
	}
	// Establish Bob's canonical write permission by an actual immutable SDK
	// original reaching FULL SQL and its exact public Saved receipt.
	if err := p.submitBob(0); err != nil {
		t.Fatal(err)
	}
	saved, updated := false, false
	for !saved || !updated {
		raw := receive(p.bob)
		var h struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &h)
		switch h.Type {
		case "commit_result":
			var result protocol.CommitResult
			_ = json.Unmarshal(raw, &result)
			if result.RequestID != "bob-grant-0" || len(result.Changes) != 1 || result.Changes[0].Status != "saved" || result.Revision != "1" {
				t.Fatalf("positive Bob write not Saved: %s", raw)
			}
			saved = true
		case "document_update":
			updated = true
		case "changes_received":
		default:
			t.Fatalf("unexpected Bob positive frame %s", raw)
		}
	}
	// Drain the owner copy before changing rights, so a later delivery cannot be
	// confused with bytes already physically sent under the old permission.
	var initial protocol.DocumentUpdate
	if raw := receive(p.owner); json.Unmarshal(raw, &initial) != nil || initial.Type != "document_update" || initial.ToRevision != "1" {
		t.Fatalf("owner missing positive cut: %s", raw)
	}
	p.changeGrant(mode)
	changedAt := time.Now()
	type tail struct {
		protected bool
		err       error
	}
	bobTail := make(chan tail, 1)
	go func() {
		_ = p.bob.SetReadDeadline(time.Now().Add(15 * time.Second))
		for {
			var raw []byte
			if err := websocket.Message.Receive(p.bob, &raw); err != nil {
				bobTail <- tail{err: err}
				return
			}
			var h struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &h)
			switch h.Type {
			case "commit_result", "document_update", "checkpoint_result":
				bobTail <- tail{protected: true}
				return
			}
		}
	}()
	// No lease increase or race-specific timing change: wait exactly the existing
	// delivery TTL, then try the retained original B and a same-owner continuation.
	<-time.After(5 * time.Second)
	t.Logf("ordinary Page grant %s effective; elapsed=%s", mode, time.Since(changedAt))
	_ = p.submitBob(1) // A closed old writer is an expected refusal, not a retry.
	var count int
	if err := p.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id='http-native-page'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("revoked Bob original reached SQL before owner continuation", count, err)
	}
	select {
	case result := <-bobTail:
		if result.protected || result.err == nil {
			t.Fatal("old writer received protected content after grant change")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("old writer remained open after Page permission change")
	}
	if err := p.oldTicket(); err == nil {
		t.Fatal("old writer ticket reconnected after grant change")
	}
	t.Log("old Bob writer closed, retained B absent from SQL, old ticket denied")
	p.ownerContinue()
	ownerSaved, ownerUpdated := false, false
	for !ownerSaved || !ownerUpdated {
		raw := receive(p.owner)
		var h struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &h)
		switch h.Type {
		case "commit_result":
			var result protocol.CommitResult
			_ = json.Unmarshal(raw, &result)
			if result.RequestID != "owner-after-grant" || len(result.Changes) != 1 || result.Changes[0].Status != "saved" || result.Revision != "2" {
				t.Fatalf("unaffected owner not Saved: %s", raw)
			}
			ownerSaved = true
		case "document_update":
			ownerUpdated = true
		case "changes_received":
		default:
			t.Fatalf("unexpected owner frame %s", raw)
		}
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id='http-native-page'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("unaffected owner did not commit exactly one continuation", count, err)
	}
	if mode == "reader" {
		p.newReader()
	}
	t.Log("actual owner grant API invalidated old Bob writer; same owner continued Saved; no Bob post-change SQL operation")
}
