package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestCollaborationAdmissionDenialIdentifiesCheckWithoutChangingDecision(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*collaborationAdmissionTestClock, collaborationHumanAdmission)
		write      bool
		remaining  time.Duration
	}{
		{"expired", "admission_expired", func(c *collaborationAdmissionTestClock, a collaborationHumanAdmission) { c.now = a.deadline }, false, 0},
		{"horizon", "admission_remaining_horizon", func(c *collaborationAdmissionTestClock, a collaborationHumanAdmission) {
			c.now = a.deadline - time.Second
		}, false, 5 * time.Second},
		{"reader", "write_capability", func(*collaborationAdmissionTestClock, collaborationHumanAdmission) {}, true, 0},
		{"boot", "clock_epoch", func(c *collaborationAdmissionTestClock, _ collaborationHumanAdmission) { c.boot = "other-boot" }, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, owner, ctx, clock := collaborationAdmissionFixture(t)
			_, admission, err := s.issue(ctx, owner, false)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			s.pages.logger = slog.New(slog.NewJSONHandler(&output, nil))
			tc.mutate(clock, admission)
			tx, err := r.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = s.checkCaptured(ctx, tx, admission, tc.write, tc.remaining)
			if err != errCollaborationAdmission {
				t.Fatalf("denial changed: %v", err)
			}
			var event map[string]any
			if err := json.Unmarshal(output.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event["failed_check"] != tc.want || event["admission_ref"] != collaborationAdmissionRef(admission) {
				t.Fatalf("wrong check/ref: %v", event)
			}
			if len(event) != 5 {
				t.Fatalf("unexpected log fields: %v", event)
			}
			for _, secret := range []string{admission.participant, admission.source.UserID, admission.source.ID} {
				if strings.Contains(output.String(), secret) {
					t.Fatal("raw authority identity entered diagnostics")
				}
			}
		})
	}
}

func TestCollaborationAdmissionDiagnosticsBoundedAndRedacted(t *testing.T) {
	var output bytes.Buffer
	s := &collaborationHumanRegistry{pages: &PageHandler{logger: slog.New(slog.NewJSONHandler(&output, nil))}}
	sentinel := errors.New("private document and credential")
	admission := collaborationHumanAdmission{participant: "private participant"}
	for i := 0; i < 100; i++ {
		if got := s.logAdmissionDenial(admission, "private command and payload", sentinel); got != sentinel {
			t.Fatal("original error changed")
		}
	}
	if strings.Count(output.String(), "\n") != 32 {
		t.Fatal("diagnostic budget not enforced")
	}
	if strings.Contains(output.String(), "private") {
		t.Fatal("untrusted value entered diagnostics")
	}
	if s.logAdmissionDenial(admission, "clock", nil) != nil {
		t.Fatal("success changed")
	}
}
