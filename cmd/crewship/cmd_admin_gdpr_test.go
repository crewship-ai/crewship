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
			return 200, []byte(`{"action_id":"act-1","deleted":{"agents":2}}`), "application/json"
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
	})
}
