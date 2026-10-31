package work

import (
	"errors"
	"testing"
)

func hostChatRequest(id string) HostChatRequest {
	return HostChatRequest{RunID: id, WorkspaceID: "ws1", AgentID: "a1", CrewID: "c1", ChatID: "chat1", UserID: "user1", TriggerMessageID: "message-" + id}
}

func TestHostChatCapacityAndIdentity(t *testing.T) {
	s, db, _ := newTestStore(t)
	r := hostChatRequest("msg_one")
	c, fresh, err := s.BeginHostChat(t.Context(), r)
	if err != nil || !fresh {
		t.Fatalf("begin: %v %v", fresh, err)
	}
	again, fresh, err := s.BeginHostChat(t.Context(), r)
	if err != nil || fresh || again.Generation != c.Generation {
		t.Fatalf("idempotency: %+v %v %v", again, fresh, err)
	}
	foreign := r
	foreign.UserID = "other"
	if _, _, err = s.BeginHostChat(t.Context(), foreign); !errors.Is(err, ErrNotBound) {
		t.Fatalf("foreign binding: %v", err)
	}
	if _, _, err = s.BeginHostChat(t.Context(), hostChatRequest("msg_two")); !errors.Is(err, ErrNoWork) {
		t.Fatalf("serial capacity bypass: %v", err)
	}
	if countRows(t, db, "work_items") != 1 || countRows(t, db, "work_attempts") != 1 {
		t.Fatal("refused capacity left queued/replayable work")
	}
	if err = s.HostChatPhase(t.Context(), r.RunID, "request"); err != nil {
		t.Fatal(err)
	}
	if err = s.HostChatPhase(t.Context(), r.RunID, "request"); err == nil {
		t.Fatal("duplicate process creation permitted")
	}
	if err = s.HostChatPhase(t.Context(), r.RunID, "started"); err != nil {
		t.Fatal(err)
	}
	zero := 0
	wrong := 99
	if err = s.FinishHostChat(t.Context(), r.RunID, HostChatResult{Status: "COMPLETED", ObservedExit: &zero, Result: RunResult{ExitCode: &wrong}}); err != nil {
		t.Fatal(err)
	}
	p, owned, err := s.RunProjection(t.Context(), r.RunID)
	if err != nil || !owned || !p.Ready || p.Status != "COMPLETED" || p.Result.ExitCode == nil || *p.Result.ExitCode != 0 {
		t.Fatalf("projection: %+v %v", p, err)
	}
	if err = s.HostChatPhase(t.Context(), r.RunID, "heartbeat"); err == nil {
		t.Fatal("ended attempt can heartbeat")
	}
	if _, _, err = s.BeginHostChat(t.Context(), r); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("ended attempt revived: %v", err)
	}
	if _, _, err = s.BeginHostChat(t.Context(), hostChatRequest("msg_two")); err != nil {
		t.Fatalf("real exit retained capacity: %v", err)
	}
}

func TestHostChatUnconfirmedExitRetainsCustody(t *testing.T) {
	s, db, _ := newTestStore(t)
	r := hostChatRequest("msg_unknown")
	initial, _, err := s.BeginHostChat(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.HostChatPhase(t.Context(), r.RunID, "request"); err != nil {
		t.Fatal(err)
	}
	displayExit := 0
	// Display exit and captured timestamp are not host-observed terminal proof.
	if err := s.FinishHostChat(t.Context(), r.RunID, HostChatResult{Status: "FAILED", Result: RunResult{CompletedAt: "2026-10-30T12:00:00Z", ExitCode: &displayExit}}); err != nil {
		t.Fatal(err)
	}
	b, _, err := s.HostChatAttempt(t.Context(), r.RunID)
	if err != nil || b.State != StateNeedsReconciliation || !b.Ended {
		t.Fatalf("custody: %+v %v", b, err)
	}
	if b.WorkID != initial.Item.ID || b.Generation != initial.Generation {
		t.Fatalf("unknown exit replaced original binding: %+v", b)
	}
	var reason string
	if err := db.QueryRowContext(t.Context(), `SELECT end_reason FROM work_attempts WHERE run_id=? AND work_id=? AND generation=?`, r.RunID, b.WorkID, b.Generation).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "protected host capture awaiting retained result" {
		t.Fatalf("recovery cutpoint changed: %q", reason)
	}
	p, _, err := s.RunProjection(t.Context(), r.RunID)
	if err != nil || p.Ready {
		t.Fatalf("unknown exit projected terminal: %+v %v", p, err)
	}
	if _, _, err = s.BeginHostChat(t.Context(), hostChatRequest("msg_next")); !errors.Is(err, ErrNoWork) {
		t.Fatalf("unknown execution released capacity: %v", err)
	}
	zero := 0
	if _, err = s.RecoverRetainedResult(t.Context(), r.RunID, RunResult{ExitCode: &zero, CompletedAt: "2026-10-30T12:00:01Z"}, true); err != nil {
		t.Fatalf("actual protected recovery: %v", err)
	}
	recovered, _, err := s.HostChatAttempt(t.Context(), r.RunID)
	if err != nil || recovered.WorkID != b.WorkID || recovered.Generation != b.Generation {
		t.Fatalf("protected recovery replaced original binding: %+v %v", recovered, err)
	}
	var attempts int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM work_attempts WHERE work_id=?`, b.WorkID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("recovery manufactured an attempt: %d %v", attempts, err)
	}
	p, _, err = s.RunProjection(t.Context(), r.RunID)
	if err != nil || !p.Ready || p.Status != "COMPLETED" {
		t.Fatalf("actual retained exit not projected: %+v %v", p, err)
	}
	if _, _, err = s.BeginHostChat(t.Context(), hostChatRequest("msg_next")); err != nil {
		t.Fatalf("protected success failed to release custody: %v", err)
	}
}

func TestHostChatNoCreationAndGateRefusalReleaseOnlyOwnAttempt(t *testing.T) {
	for _, requested := range []bool{false, true} {
		t.Run(map[bool]string{false: "never_requested", true: "gate_refused"}[requested], func(t *testing.T) {
			s, _, _ := newTestStore(t)
			r := hostChatRequest("msg_refused")
			if _, _, err := s.BeginHostChat(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if requested {
				if err := s.HostChatPhase(t.Context(), r.RunID, "request"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.FinishHostChat(t.Context(), r.RunID, HostChatResult{Status: "FAILED", ExecRefused: requested}); err != nil {
				t.Fatal(err)
			}
			p, _, err := s.RunProjection(t.Context(), r.RunID)
			if err != nil || !p.Ready || p.Status != "FAILED" {
				t.Fatalf("refusal: %+v %v", p, err)
			}
			if _, _, err = s.BeginHostChat(t.Context(), hostChatRequest("msg_next")); err != nil {
				t.Fatalf("refusal retained capacity: %v", err)
			}
		})
	}
}

func TestHostChatConcurrentAdmissionsNeverOverbook(t *testing.T) {
	s, db, _ := newTestStore(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{"msg_concurrent_one", "msg_concurrent_two"} {
		go func(id string) {
			<-start
			_, _, err := s.BeginHostChat(t.Context(), hostChatRequest(id))
			results <- err
		}(id)
	}
	close(start)
	successes := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrNoWork) {
			t.Fatalf("unexpected concurrent admission error: %v", err)
		}
	}
	if successes != 1 || countRows(t, db, "work_attempts") != 1 || countRows(t, db, "work_items") != 1 {
		t.Fatalf("concurrent admissions=%d; refused work must roll back", successes)
	}
}

func TestHostChatStaleGenerationCannotReenter(t *testing.T) {
	s, db, _ := newTestStore(t)
	r := hostChatRequest("msg_stale")
	c, _, err := s.BeginHostChat(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE work_items SET generation=generation+1 WHERE id=?`, c.Item.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.HostChatPhase(t.Context(), r.RunID, "request"); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale creation: %v", err)
	}
	zero := 0
	if err = s.FinishHostChat(t.Context(), r.RunID, HostChatResult{Status: "COMPLETED", ObservedExit: &zero}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale exit settled newer generation: %v", err)
	}
}
