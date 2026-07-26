package main

// CLI parity for the memory-config admin endpoint (#1379). The behaviour worth
// pinning: `get` must say whether the value is a deliberate setting or the
// built-in default, and `set` must PATCH (partial) rather than replace, so an
// older binary can't drop a setting it doesn't model.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const memCfgPath = "/api/v1/admin/memory/config"

func TestAdminMemoryConfigGet(t *testing.T) {
	t.Run("distinguishes a default from an explicit setting", func(t *testing.T) {
		// "30 days" alone doesn't tell an operator whether anyone chose it —
		// and that decides whether changing it is routine or is overriding
		// somebody's policy.
		stub := covStub(t)
		stub.OnGet(memCfgPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"workspace_id":"ws1","versions_retention_days":30,"is_default":true}`), "application/json"
		})
		covResetFlags(t, adminMemoryConfigGetCmd)
		out := covCaptureAll(t, func() {
			if err := adminMemoryConfigGetCmd.RunE(adminMemoryConfigGetCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "30 days") {
			t.Errorf("value missing:\n%s", out)
		}
		if !strings.Contains(out, "default") {
			t.Errorf("must say the value is a default:\n%s", out)
		}
	})

	t.Run("explicit setting reads as explicit", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(memCfgPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"workspace_id":"ws1","versions_retention_days":7,"is_default":false}`), "application/json"
		})
		covResetFlags(t, adminMemoryConfigGetCmd)
		out := covCaptureAll(t, func() {
			if err := adminMemoryConfigGetCmd.RunE(adminMemoryConfigGetCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "explicit") {
			t.Errorf("must distinguish an explicit setting:\n%s", out)
		}
	})

	t.Run("surfaces unmodelled keys from the stored document", func(t *testing.T) {
		// The stored doc can carry settings this CLI version doesn't know.
		// Hiding them would make `get` look authoritative when it isn't.
		stub := covStub(t)
		stub.OnGet(memCfgPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"workspace_id":"ws1","versions_retention_days":7,"is_default":false,
				"raw_config":"{\"versions_retention_days\":7,\"future_knob\":\"x\"}"}`), "application/json"
		})
		covResetFlags(t, adminMemoryConfigGetCmd)
		out := covCaptureAll(t, func() {
			if err := adminMemoryConfigGetCmd.RunE(adminMemoryConfigGetCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "future_knob") {
			t.Errorf("an unmodelled stored key must still be visible:\n%s", out)
		}
	})

	t.Run("403 propagates", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(memCfgPath, clitest.ErrorResponse(403, "admin role required"))
		covResetFlags(t, adminMemoryConfigGetCmd)
		err := adminMemoryConfigGetCmd.RunE(adminMemoryConfigGetCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "admin role required") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestAdminMemoryConfigSet(t *testing.T) {
	t.Run("requires --retention-days", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, adminMemoryConfigSetCmd)
		err := adminMemoryConfigSetCmd.RunE(adminMemoryConfigSetCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "--retention-days") {
			t.Fatalf("expected the required-flag error, got %v", err)
		}
	})

	t.Run("PATCHes only the one key", func(t *testing.T) {
		// Partial by design: the server merges and keeps keys this CLI doesn't
		// model, so an older binary can't silently drop a newer setting.
		stub := covStub(t)
		stub.OnPatch(memCfgPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"workspace_id":"ws1","versions_retention_days":30,"is_default":false}`), "application/json"
		})
		covResetFlags(t, adminMemoryConfigSetCmd)
		if err := adminMemoryConfigSetCmd.Flags().Set("retention-days", "30"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		covCaptureAll(t, func() {
			if err := adminMemoryConfigSetCmd.RunE(adminMemoryConfigSetCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		calls := stub.CallsFor("PATCH", memCfgPath)
		if len(calls) != 1 {
			t.Fatalf("want 1 PATCH, got %d", len(calls))
		}
		var body map[string]any
		if err := json.Unmarshal(calls[0].Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["versions_retention_days"] != float64(30) {
			t.Errorf("versions_retention_days = %v", body["versions_retention_days"])
		}
		if len(body) != 1 {
			t.Errorf("must send ONLY the patched key, got %v", body)
		}
	})

	t.Run("server validation error propagates", func(t *testing.T) {
		// The bounds live on the server (1..3650); the CLI must relay its
		// message rather than duplicating the rule and drifting from it.
		stub := covStub(t)
		stub.OnPatch(memCfgPath, clitest.ErrorResponse(400, "versions_retention_days must be >= 1, got 0"))
		covResetFlags(t, adminMemoryConfigSetCmd)
		if err := adminMemoryConfigSetCmd.Flags().Set("retention-days", "0"); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		err := adminMemoryConfigSetCmd.RunE(adminMemoryConfigSetCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "must be >= 1") {
			t.Fatalf("got %v", err)
		}
	})
}
