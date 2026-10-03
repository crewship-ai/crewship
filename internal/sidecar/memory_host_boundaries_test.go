package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestMemoryHostRejectsInvalidMutationsBeforeSending(t *testing.T) {
	var calls atomic.Int64
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected request", 500) }))
	defer host.Close()
	s, local := newR6Sidecar(t, host.URL)
	t.Setenv("CREWSHIP_MEMORY_REQUIRE_GUARANTEED", "true")
	for _, tc := range []struct{ name, tool, args, code string }{
		{"invalid write shape", "memory.write", `[]`, "invalid_args"},
		{"invalid daily shape", "memory.append_daily", `[]`, "invalid_args"},
		{"empty daily entry", "memory.append_daily", `{"entry":""}`, "invalid_args"},
		{"bad mode", "memory.write", `{"tier":"AGENT","mode":"erase","content":"new"}`, "invalid_args"},
		{"empty content", "memory.write", `{"tier":"AGENT","mode":"append","content":" "}`, "invalid_args"},
		{"missing operation", "memory.write", `{"tier":"AGENT","mode":"append","content":"new"}`, "operation_id_required"},
		{"missing removals", "memory.write", `{"tier":"AGENT","mode":"replace","content":"new","operation_id":"op"}`, "removals_required"},
		{"missing revision", "memory.write", `{"tier":"AGENT","mode":"replace","content":"new","operation_id":"op","removals":[]}`, "expected_revision_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/mcp/memory", nil)
			r.Header.Set("Authorization", "Bearer "+r6RunToken(r6RunA))
			result, handled := s.memoryMCPViaHost(r, tc.tool, json.RawMessage(tc.args))
			if !handled || !result.IsError || len(result.Content) != 1 {
				t.Fatalf("invalid request was not rejected: %+v handled=%v", result, handled)
			}
			var detail map[string]any
			if json.Unmarshal([]byte(result.Content[0].Text), &detail) != nil || detail["error_code"] != tc.code {
				t.Fatalf("error = %s", result.Content[0].Text)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid mutations contacted host %d times", calls.Load())
	}
	if _, err := os.Stat(filepath.Join(local, "AGENT.md")); !os.IsNotExist(err) {
		t.Fatalf("invalid mutation created local memory: %v", err)
	}
}

func TestMemoryHostReadRelaysRefusalAndFallsBackOnlyForUnavailableCanonicalRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		handled bool
	}{
		{"refused", 403, `{"error_code":"run_revoked"}`, true},
		{"older host", 404, `not found`, false},
		{"malformed canonical", 200, `not-json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != memoryHostCanonicalPath || r.URL.Query().Get("scope") != "agent" || r.URL.Query().Get("file") != "pins.md" {
					t.Errorf("wrong canonical request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer host.Close()
			s, _ := newR6Sidecar(t, host.URL)
			r := httptest.NewRequest("POST", "/mcp/memory", nil)
			r.Header.Set("Authorization", "Bearer "+r6RunToken(r6RunA))
			result, handled := s.memoryMCPViaHost(r, "memory.read", json.RawMessage(`{"tier":"pins"}`))
			if handled != tc.handled {
				t.Fatalf("handled=%v want %v: %+v", handled, tc.handled, result)
			}
			if tc.handled && (!result.IsError || len(result.Content) != 1 || result.Content[0].Text != tc.body) {
				t.Fatalf("host refusal changed: %+v", result)
			}
		})
	}
}

func TestMemoryHostReadInvalidArgumentsAndUnavailableHost(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("closed host must not receive request") }))
	host.Close()
	s, _ := newR6Sidecar(t, host.URL)
	r := httptest.NewRequest("POST", "/mcp/memory", nil)
	r.Header.Set("Authorization", "Bearer "+r6RunToken(r6RunA))
	for _, tc := range []struct {
		args    string
		handled bool
	}{
		{`[]`, true},
		{`{"tier":"PERSONA"}`, false},
		{`{"tier":"daily","key":"../escape"}`, false},
		{`{"tier":"AGENT"}`, false},
	} {
		got, handled := s.memoryMCPViaHost(r, "memory.read", json.RawMessage(tc.args))
		if handled != tc.handled || handled && !got.IsError {
			t.Fatalf("args=%s handled=%v result=%+v", tc.args, handled, got)
		}
	}
}

func TestMemoryHostLegacyOperationIDIsExplicitlyNotRetrySafe(t *testing.T) {
	t.Setenv("CREWSHIP_MEMORY_REQUIRE_GUARANTEED", "false")
	host := newR6Host(t)
	host.seedAttempt(t, "work-generated-operation", r6RunA, 1)
	s, _ := newR6Sidecar(t, host.srv.URL)
	result := mcpCall(t, s, r6RunToken(r6RunA), "memory.write", map[string]any{"tier": "AGENT", "mode": "append", "content": "legacy append\n"})
	if result.IsError {
		t.Fatalf("legacy append rejected: %+v", result)
	}
	meta := result.meta(t)
	operation, ok := meta["operation_id"].(string)
	if !ok || operation == "" || meta["retry_safe"] != false {
		t.Fatalf("generated operation misrepresented retry safety: %+v", meta)
	}
	if got := host.hostFile(t, "AGENT.md"); got != "legacy append\n" {
		t.Fatalf("append not committed on host: %q", got)
	}
	if n, run := host.mutations(t, operation); n != 1 || run != r6RunA {
		t.Fatalf("wrong ledger attribution: n=%d run=%s", n, run)
	}
}
