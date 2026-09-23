package work

import (
	"errors"
	"testing"
)

func TestCheckIngress_ScheduledInputSharesWebhookByteBudget(t *testing.T) {
	s, db, _ := newTestStore(t)
	input := `{"prompt":"žluťoučký"}`
	req := backgroundReq("a1")
	req.Source = SourceSchedule
	req.InputJSON = input
	r := accept(t, s, db, req)
	lim := IngressLimits{WorkspaceRawBytes: int64(len(input) + 1)}
	// UTF-8 bytes count, not code points. The next webhook body consumes the
	// same budget even though the scheduled work has no delivery row.
	err := checkIngress(t, s, db, lim, IngressRequest{WorkspaceID: "ws1", EndpointID: "ep", BodyBytes: 2})
	if !errors.Is(err, ErrWorkspaceBytesFull) {
		t.Fatalf("scheduled input did not count toward bytes: %v", err)
	}
	if err := checkIngress(t, s, db, lim, IngressRequest{WorkspaceID: "ws1", EndpointID: "ep", BodyBytes: 1}); err != nil {
		t.Fatalf("exact boundary refused: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE work_items SET state='cancelled', terminal_at=updated_at WHERE id=?`, r.WorkID); err != nil {
		t.Fatal(err)
	}
	if err := checkIngress(t, s, db, lim, IngressRequest{WorkspaceID: "ws1", EndpointID: "ep", BodyBytes: 2}); err != nil {
		t.Fatalf("terminal input still holds ingress bytes: %v", err)
	}
}
