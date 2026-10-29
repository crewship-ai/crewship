//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"golang.org/x/net/websocket"
)

// The tokens are synthetic signed JWT fixtures; transport, native preparation,
// Page grant administration, source admission and Saved outcomes are real.
func TestMooringHTTPActualPageGrantRevocation(t *testing.T) {
	for _, mode := range []string{"revoke", "reader", "late-ticket", "bounded-revoke", "bounded-reader"} {
		t.Run(mode, func(t *testing.T) { runMooringHTTPActualSDK(t, false, mode) })
	}
}

type mooringPageGrantProbe struct {
	db            *sql.DB
	owner, bob    *websocket.Conn
	submitBob     func(int) error
	changeGrant   func(string) time.Time
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
	if strings.HasPrefix(mode, "bounded-") {
		runMooringBoundedGrantWindow(t, strings.TrimPrefix(mode, "bounded-"), p)
		return
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

// No sleeps, renewed leases or race-specific allowance: this window begins
// BEFORE the canonical permission request, conservatively before its SQL commit.
// The ordinary post-grant modes above remain separate semantic controls.
func runMooringBoundedGrantWindow(t *testing.T, mode string, p mooringPageGrantProbe) {
	t.Helper()
	type observation struct {
		at   time.Time
		kind string
		err  error
	}
	events := make(chan observation, 128)
	entered := make(chan struct{})
	monitorCtx, stop := context.WithCancel(t.Context())
	monitorDone := make(chan struct{})
	defer func() { stop(); _ = p.bob.SetReadDeadline(time.Now()); <-monitorDone }()
	// This is the ONLY Bob reader, started before the permission mutation.
	go func() {
		defer close(monitorDone)
		close(entered)
		for {
			var raw []byte
			err := websocket.Message.Receive(p.bob, &raw)
			event := observation{at: time.Now(), err: err}
			if err == nil {
				var h struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(raw, &h) != nil {
					event.kind = "invalid"
				} else {
					event.kind = h.Type
				}
			}
			select {
			case events <- event:
			case <-monitorCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	<-entered
	started := p.changeGrant(mode)
	limit := started.Add(5 * time.Second)
	_ = p.bob.SetReadDeadline(limit)
	committedBy := time.Now()
	// SAME owner socket and original immutable owner packet. No reopen/recovery.
	p.ownerContinue()
	_ = p.owner.SetReadDeadline(limit)
	_ = p.submitBob(1)
	ownerSaved, ownerUpdate := false, false
	for !ownerSaved || !ownerUpdate {
		var raw []byte
		if err := websocket.Message.Receive(p.owner, &raw); err != nil {
			t.Fatalf("owner SAMEWS not Saved inside five-second revoke window: elapsed=%s error=%v", time.Since(started), err)
		}
		var h struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &h) != nil {
			t.Fatal("invalid owner frame")
		}
		switch h.Type {
		case "changes_received":
		case "commit_result":
			var r protocol.CommitResult
			if json.Unmarshal(raw, &r) != nil || r.RequestID != "owner-after-grant" || len(r.Changes) != 1 || r.Changes[0].Status != "saved" || r.Revision != "2" {
				t.Fatal("owner continuation not exact SQL Saved")
			}
			ownerSaved = true
		case "document_update":
			var u protocol.DocumentUpdate
			if json.Unmarshal(raw, &u) != nil || u.FromRevision != "1" || u.ToRevision != "2" {
				t.Fatal("owner continuation cut mismatch")
			}
			ownerUpdate = true
		default:
			t.Fatalf("unexpected owner frame type %q", h.Type)
		}
		if time.Now().After(limit) {
			t.Fatal("owner Saved exceeded five seconds")
		}
	}
	ownerElapsed := time.Since(started)
	closed := false
	processObservation := func(e observation) {
		if e.err != nil {
			var ne net.Error
			if errors.As(e.err, &ne) && ne.Timeout() {
				t.Fatal("Bob read timeout is not physical close evidence")
			}
			if !errors.Is(e.err, io.EOF) && !errors.Is(e.err, io.ErrUnexpectedEOF) {
				t.Fatalf("Bob closure not established: %T", e.err)
			}
			if e.at.Before(started) {
				t.Fatal("Bob was already closed before timed write revocation")
			}
			if e.at.After(limit) {
				t.Fatal("Bob physical close exceeded five seconds")
			}
			closed = true
			t.Logf("bounded %s ownerSaved=%s BobClose=%s permissionRequest=%s", mode, ownerElapsed, e.at.Sub(started), committedBy.Sub(started))
			return
		}
		switch e.kind {
		case "document_update", "checkpoint_result":
			if mode == "reader" {
				return
			} // Independent READ survives write downgrade.
			fallthrough
		case "commit_result", "invalid":
			t.Fatalf("Bob received protected frame during revoke window: type=%s afterResponse=%t", e.kind, !e.at.Before(committedBy))
		}
	}
	closeDeadline := time.NewTimer(time.Until(limit))
	defer closeDeadline.Stop()
	for !closed {
		// Prefer observations already captured by the sole reader. If both EOF and
		// the absolute deadline are ready, do not turn a timely EOF into false RED.
		select {
		case e := <-events:
			processObservation(e)
			continue
		default:
		}
		select {
		case e := <-events:
			processObservation(e)
		case <-closeDeadline.C:
			draining := true
			for draining {
				select {
				case e := <-events:
					processObservation(e)
				default:
					draining = false
				}
			}
			if !closed {
				t.Fatal("Bob physical close not observed within five seconds")
			}
		}
	}
	// Even an early physical close is not a final SQL outcome: an already queued
	// mutation must remain absent through the ORIGINAL absolute five-second horizon.
	if left := time.Until(limit); left > 0 {
		horizon := time.NewTimer(left)
		defer horizon.Stop()
		select {
		case <-horizon.C:
		case <-t.Context().Done():
			t.Fatal("test canceled before revoke horizon")
		}
	}
	if mode == "reader" {
		var readRows, writeRows int
		if err := p.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id='http-native-page' AND subject_type='user' AND level='read'`).Scan(&readRows); err != nil || readRows != 1 {
			t.Fatal("downgrade did not retain independent READ row", readRows, err)
		}
		if err := p.db.QueryRow(`SELECT count(*) FROM page_grants WHERE page_id='http-native-page' AND subject_type='user' AND level='write'`).Scan(&writeRows); err != nil || writeRows != 0 {
			t.Fatal("downgrade retained writer grant", writeRows, err)
		}
	}
	var count int
	if err := p.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id='http-native-page'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("Bob original committed or owner continuation missing", count, err)
	}
	var bobB int
	if err := p.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id='http-native-page' AND op_id='peer-http-op-1'`).Scan(&bobB); err != nil || bobB != 0 {
		t.Fatal("post-revoke Bob original reached SQL", bobB, err)
	}
	var ownerOps int
	if err := p.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations WHERE page_id='http-native-page' AND op_id='own-http-op-0'`).Scan(&ownerOps); err != nil || ownerOps != 1 {
		t.Fatal("owner original missing at revoke horizon", ownerOps, err)
	}
	// This is semantic stale-ticket evidence after the bounded close window,
	// not a claim about additional elapsed time. Existing dial has its own bound.
	if err := p.oldTicket(); err == nil {
		t.Fatal("old writer ticket admitted after revoke")
	} else {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("old ticket timeout is not definite rejection")
		}
	}
	if mode == "reader" {
		p.newReader()
	}
}
