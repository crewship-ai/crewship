package api

import (
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestDocumentPreviewAdmissionRejectsMixedCreationAuthority(t *testing.T) {
	for _, field := range []string{"artifact_request_id", "board_request_id", "artifact_human_receipt", "artifact_human_message_id"} {
		t.Run(field, func(t *testing.T) {
			rig := newDocumentRunAdmissionRig(t)
			var status string
			if err := rig.f.rig.db.QueryRow(`SELECT status FROM agents WHERE id=?`, rig.f.a.AgentID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			var observed atomic.Int64
			rig.writer.AddCommitObserver(func(entries []journal.Entry) {
				for _, entry := range entries {
					if entry.Type == journal.EntryRunStarted {
						observed.Add(1)
					}
				}
			})
			response := rig.call(t, func(body map[string]any) {
				if field == "artifact_human_receipt" {
					body[field] = rig.f.receipt
				} else {
					body[field] = "selected-request"
				}
			}, false)
			if response.Code != 403 {
				t.Errorf("mixed creation authority status%d want403", response.Code)
			}
			rig.assertNoAdmission(t, observed.Load(), status)
		})
	}
}
