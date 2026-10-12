package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/webhook"
	"github.com/crewship-ai/crewship/internal/work"
)

// budgetExhaustedAcceptor is what the acceptance handle looks like when the
// original delivery's run holds SQLite's write lock past the 2 s budget — the
// condition under which linux-arm64 answered a duplicate with 503 (#2964).
type budgetExhaustedAcceptor struct{}

func (budgetExhaustedAcceptor) Do(context.Context, func(context.Context, *sql.Tx) error) error {
	return fmt.Errorf("%w after 2s waiting to begin: database is locked", work.ErrAcceptanceBudget)
}

// TestVerticalServer_ADuplicateIsAnsweredFromTheLedgerWhenTheWriteCannotCommit
// pins #2964: a re-delivery of accepted work gets its original receipt even
// when the acceptance write transaction cannot run, and only genuinely new
// work is refused with 503.
func TestVerticalServer_ADuplicateIsAnsweredFromTheLedgerWhenTheWriteCannotCommit(t *testing.T) {
	rig := newVerticalRig(t)
	rig.proc.hold = make(chan struct{})
	rig.startDispatcher()

	first := rig.deliver(verticalBody)
	rig.waitForState(first.WorkID, work.StateRunning)

	rig.router.webhookHandler.acceptRunner = budgetExhaustedAcceptor{}

	second := rig.deliver(verticalBody)
	if second.Status != http.StatusAccepted || !second.Duplicate || second.WorkID != first.WorkID {
		t.Fatalf("duplicate under write contention: %+v, want 202 duplicate of %s", second, first.WorkID)
	}
	if second.State != string(work.StateRunning) {
		t.Errorf("duplicate answered state %q, want the work's current %q", second.State, work.StateRunning)
	}

	// New work cannot be recorded, so it is refused — never a false 202.
	body := `{"event":"deploy","source":"github","data":{"ref":"release"}}`
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/webhooks/%s/%s/trigger", rig.baseURL, rig.crewID, rig.agentID), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", webhook.ComputeHMAC([]byte(body), rig.secret))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("new delivery under write contention: %d %s, want 503", resp.StatusCode, raw)
	}

	close(rig.proc.hold)
	rig.waitForState(first.WorkID, work.StateSucceeded)
	if n := rig.count(`SELECT COUNT(*) FROM work_items`); n != 1 {
		t.Errorf("%d work items, want 1", n)
	}
}
