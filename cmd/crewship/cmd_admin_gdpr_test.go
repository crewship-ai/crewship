package main

// CLI parity for the GDPR endpoints (CLAUDE.md rule 3). These are the two
// operations that answer a legal request — a copy of what is held about a
// person, or its erasure — and there was no way to run either from a
// terminal, or to script one for a queue of requests.

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminGDPRExport(t *testing.T) {
	t.Run("scopes the request to the workspace", func(t *testing.T) {
		stub := covStub(t)
		var gotPath string
		stub.OnGet("/api/v1/admin/users/u-1/data", func(r *http.Request, _ []byte) (int, []byte, string) {
			gotPath = r.URL.String()
			return 200, []byte(`{"user":{"id":"u-1","email":"a@b.c"},"rows":{"agents":2}}`), "application/json"
		})
		covResetFlags(t, adminGDPRExportCmd)
		out := covCaptureAll(t, func() {
			if err := adminGDPRExportCmd.RunE(adminGDPRExportCmd, []string{"u-1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		// The admin API is workspace-scoped by middleware; without this the
		// server answers 400 before the handler runs.
		if !strings.Contains(gotPath, "workspace_id=") {
			t.Errorf("request was not workspace-scoped: %s", gotPath)
		}
		if !strings.Contains(out, "a@b.c") {
			t.Errorf("export body not surfaced:\n%s", out)
		}
	})
}

func TestAdminGDPRDelete(t *testing.T) {
	t.Run("refuses without a reason — the reason IS the audit trail", func(t *testing.T) {
		covResetFlags(t, adminGDPRDeleteCmd)
		_ = covCaptureAll(t, func() {
			err := adminGDPRDeleteCmd.RunE(adminGDPRDeleteCmd, []string{"u-1"})
			if err == nil {
				t.Error("delete without --reason was allowed")
			} else if !strings.Contains(err.Error(), "reason") {
				t.Errorf("error does not name the missing reason: %v", err)
			}
		})
	})

	t.Run("refuses without an explicit confirmation", func(t *testing.T) {
		covResetFlags(t, adminGDPRDeleteCmd)
		_ = adminGDPRDeleteCmd.Flags().Set("reason", "SAR #1234")
		_ = covCaptureAll(t, func() {
			err := adminGDPRDeleteCmd.RunE(adminGDPRDeleteCmd, []string{"u-1"})
			if err == nil {
				t.Error("an irreversible delete ran without --yes")
			}
		})
	})

	t.Run("sends the reason and the workspace", func(t *testing.T) {
		stub := covStub(t)
		var gotPath, gotBody string
		stub.OnDelete("/api/v1/admin/users/u-1/data", func(r *http.Request, body []byte) (int, []byte, string) {
			gotPath, gotBody = r.URL.String(), string(body)
			// The handler's real payload (AdminGDPRHandler.DeleteUserData):
			// rows_deleted plus a per-table scope, not a `deleted` map.
			return 202, []byte(`{"action_id":"act-1","data_subject":"u-1","workspace_id":"ws-1",` +
				`"rows_deleted":2,"scope":{"memory_versions":2}}`), "application/json"
		})
		covResetFlags(t, adminGDPRDeleteCmd)
		_ = adminGDPRDeleteCmd.Flags().Set("reason", "SAR #1234")
		_ = adminGDPRDeleteCmd.Flags().Set("yes", "true")
		out := covCaptureAll(t, func() {
			if err := adminGDPRDeleteCmd.RunE(adminGDPRDeleteCmd, []string{"u-1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(gotPath, "workspace_id=") {
			t.Errorf("delete was not workspace-scoped: %s", gotPath)
		}
		if !strings.Contains(gotBody, "SAR #1234") {
			t.Errorf("reason not sent: %s", gotBody)
		}
		// The action id is the receipt — it is what proves the request was
		// handled, so it has to come back to the operator.
		if !strings.Contains(out, "act-1") {
			t.Errorf("action id not surfaced:\n%s", out)
		}
		// The breakdown the handler actually sends must survive decoding.
		if !strings.Contains(out, "memory_versions") {
			t.Errorf("per-table scope not surfaced:\n%s", out)
		}
	})

	// #1976: the erasure now also unnames the subject on four Pages tables,
	// with a different verb per table — a version is anonymised (the row
	// stays), a grant/link/webhook is revoked (the row goes). The receipt an
	// operator pastes into a SAR ticket has to say which happened, so the
	// scope keys carry the verb and the CLI must print them as sent.
	t.Run("surfaces the per-table Pages verbs in the receipt", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete("/api/v1/admin/users/u-1/data", func(r *http.Request, body []byte) (int, []byte, string) {
			return 202, []byte(`{"action_id":"act-3","data_subject":"u-1","workspace_id":"ws-1",` +
				`"rows_deleted":4,"scope":{"page_versions_anonymised":3,"page_grants_removed":2,` +
				`"page_public_tokens_revoked":1,"page_webhooks_revoked":1}}`), "application/json"
		})
		covResetFlags(t, adminGDPRDeleteCmd)
		_ = adminGDPRDeleteCmd.Flags().Set("reason", "SAR #1976")
		_ = adminGDPRDeleteCmd.Flags().Set("yes", "true")
		out := covCaptureAll(t, func() {
			if err := adminGDPRDeleteCmd.RunE(adminGDPRDeleteCmd, []string{"u-1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		for _, want := range []string{
			"page_versions_anonymised",
			"page_grants_removed",
			"page_public_tokens_revoked",
			"page_webhooks_revoked",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("receipt does not report %s:\n%s", want, out)
			}
		}
		// An anonymised version is not a deleted row, so the total must not
		// silently absorb it — 4 is what the server counted as removed.
		if !strings.Contains(out, "Erased 4 rows") {
			t.Errorf("row total misreported:\n%s", out)
		}
	})

	// 207 is a 2xx, so CheckError passes it through: without an explicit
	// check the operator is told the erasure succeeded when part of it
	// failed, and a scripted SAR queue records the ticket as closed.
	t.Run("partial erasure warns and fails instead of reporting success", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete("/api/v1/admin/users/u-1/data", func(r *http.Request, body []byte) (int, []byte, string) {
			return 207, []byte(`{"action_id":"act-2","rows_deleted":1,` +
				`"scope":{"memory_versions":1},"error":"delete inbox_items: database is locked"}`), "application/json"
		})
		covResetFlags(t, adminGDPRDeleteCmd)
		_ = adminGDPRDeleteCmd.Flags().Set("reason", "SAR #1234")
		_ = adminGDPRDeleteCmd.Flags().Set("yes", "true")
		var runErr error
		out := covCaptureAll(t, func() {
			runErr = adminGDPRDeleteCmd.RunE(adminGDPRDeleteCmd, []string{"u-1"})
		})
		if runErr == nil {
			t.Fatal("a partial erasure exited 0 — the operator is told it is done")
		}
		if !strings.Contains(runErr.Error(), "inbox_items") {
			t.Errorf("error does not name what failed: %v", runErr)
		}
		if !strings.Contains(out, "act-2") {
			t.Errorf("audit action id not surfaced on partial erasure:\n%s", out)
		}
	})
}
