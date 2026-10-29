package ws

import (
	"context"
	"testing"
)

// A queued workbook.committed push is rechecked against the recipient's
// current access to the private workbook Page before it is written, so it is
// never delivered after that access is revoked.
func TestWorkbookCommittedFrameIsNotDeliveredAfterRevocation(t *testing.T) {
	for _, tc := range []struct{ name, user, revoke string }{
		{"owner removed from workspace", "bench-user-0", `DELETE FROM workspace_members WHERE user_id='bench-user-0' AND workspace_id='bench-workspace-0'`},
		{"owner suspended", "bench-user-0", `UPDATE users SET suspended_at='2026-10-22T00:00:00Z' WHERE id='bench-user-0'`},
		{"page transferred", "bench-user-0", `UPDATE pages SET owner_user_id='bench-user-2' WHERE id='workbook-page'`},
		{"workspace OWNER demoted", "bench-user-1", `UPDATE workspace_members SET role='ADMIN' WHERE user_id='bench-user-1' AND workspace_id='bench-workspace-0'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := deliveryAuthorizationFixture(t, 3, 1)
			for _, q := range []string{
				`UPDATE workspace_members SET role='OWNER' WHERE user_id='bench-user-1' AND workspace_id='bench-workspace-0'`,
				`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('workbook-page','bench-workspace-0','workbook-page','Workbook','bench-user-0','{}',1)`,
			} {
				if _, err := db.ExecContext(t.Context(), q); err != nil {
					t.Fatal(err)
				}
			}
			hub := newRunningHub(t)
			hub.SetChannelAuthorizer(NewDBChannelAuthorizer(db))
			client := &Client{hub: hub, conn: dialDeliverySink(t), userID: tc.user, ctx: context.Background()}
			frame := []byte(`{"type":"workbook.committed","channel":"page:workbook-page","payload":{"page_id":"workbook-page","rev":2,"generation":1,"kind":"cells","cells":1}}`)
			if !client.writeFrame(frame) {
				t.Fatal("authorized push denied")
			}
			outsider := &Client{hub: hub, conn: dialDeliverySink(t), userID: "bench-user-2", ctx: context.Background()}
			if outsider.writeFrame(frame) {
				t.Fatal("push delivered to a member without access to the private Page")
			}
			if _, err := db.ExecContext(t.Context(), tc.revoke); err != nil {
				t.Fatal(err)
			}
			if client.writeFrame(frame) {
				t.Fatal("push delivered after access was revoked")
			}
		})
	}
}
