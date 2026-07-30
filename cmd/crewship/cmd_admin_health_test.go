package main

// CLI parity for GET /api/v1/admin/health (CLAUDE.md rule 3: an API without a
// CLI command is an API no agent can drive). The endpoint has always returned
// disk headroom, the live log level and where the master key came from; until
// now the only reader was the admin overview, which rendered two of the five
// fields.

import (
	"net/http"
	"strings"
	"testing"
)

const adminHealthPath = "/api/v1/admin/health"

func runAdminHealth(t *testing.T) string {
	t.Helper()
	covResetFlags(t, adminHealthCmd)
	return covCaptureAll(t, func() {
		if err := adminHealthCmd.RunE(adminHealthCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
}

func TestAdminHealth(t *testing.T) {
	t.Run("says when the log level is a temporary override", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(adminHealthPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"uptime_seconds":60,"db":{"connected":true},
				"log_level":{"level":"debug","baseline":"info","expires_at":"2026-07-29T14:00:00Z"}}`), "application/json"
		})
		out := runAdminHealth(t)
		if !strings.Contains(out, "temporary") || !strings.Contains(out, "info") {
			t.Errorf("a reverting override reads as permanent:\n%s", out)
		}
	})

	// The log_level fixture below is an OBJECT because that is what the server
	// sends (level + baseline + optional expiry). The first cut of this test
	// asserted a bare string, passed, and the command then failed against a
	// real instance — a fixture is only as good as the shape it copies.
	t.Run("reports uptime, database, disk, log level and key source", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(adminHealthPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"uptime_seconds":93784,"log_level":{"level":"info","baseline":"info"},
				"encryption_key_source":"generated",
				"db":{"connected":true},
				"disk":{"path":"/var/lib/crewship","free_bytes":15300000000,
				        "total_bytes":48000000000,"used_pct":68.1}}`), "application/json"
		})
		out := runAdminHealth(t)
		for _, want := range []string{"1d 2h", "connected", "info", "68", "/var/lib/crewship"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})

	// "generated" means the master key was auto-created next to the database,
	// so a copied disk carries the ciphertext AND the key that opens it. The
	// word alone does not say that; the output has to.
	t.Run("says what a generated key means, not just the word", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(adminHealthPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"uptime_seconds":60,"encryption_key_source":"generated","db":{"connected":true}}`), "application/json"
		})
		out := runAdminHealth(t)
		if !strings.Contains(strings.ToLower(out), "beside the database") {
			t.Errorf("a generated key is reported without its consequence:\n%s", out)
		}
	})

	t.Run("an unreachable database is not reported as fine", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(adminHealthPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"uptime_seconds":10,"db":{"connected":false,"error":"database is locked"}}`), "application/json"
		})
		out := runAdminHealth(t)
		if !strings.Contains(out, "database is locked") || !strings.Contains(strings.ToUpper(out), "UNREACHABLE") {
			t.Errorf("db failure not surfaced:\n%s", out)
		}
	})

	// Disk info is absent on platforms where statfs fails, and the endpoint
	// says so with an error instead of a size. Printing "0 B free" there
	// would invent an emergency.
	t.Run("absent disk info is absent, not zero", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(adminHealthPath, func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(`{"uptime_seconds":10,"db":{"connected":true},"disk":{"error":"statfs: not supported"}}`), "application/json"
		})
		out := runAdminHealth(t)
		if strings.Contains(out, "0 B") {
			t.Errorf("missing disk info rendered as zero:\n%s", out)
		}
		if !strings.Contains(out, "statfs: not supported") {
			t.Errorf("disk error not surfaced:\n%s", out)
		}
	})
}
