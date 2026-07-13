package sidecar

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1046/#1058: sidecar JSON decodes were uncapped — a compromised agent could
// OOM the sidecar with a multi-GB body. decodeCappedJSON bounds the body and
// responds 413 on overflow.

func TestDecodeCappedJSON_OversizedBody_413(t *testing.T) {
	big := bytes.NewReader([]byte(`{"x":"` + strings.Repeat("a", (sidecarMaxBodyBytes+1024)) + `"}`))
	r := httptest.NewRequest("POST", "/x", big)
	w := httptest.NewRecorder()
	var dst map[string]any
	if decodeCappedJSON(w, r, &dst) {
		t.Fatal("oversized body should not decode successfully")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

// #1116 self-review [4]: pipeline data-plane bodies can exceed the 1 MiB
// control-plane default; decodeCappedJSONLimit accepts up to its larger cap.
func TestDecodeCappedJSONLimit_AcceptsAboveDefault(t *testing.T) {
	// ~2 MiB — over sidecarMaxBodyBytes (1 MiB) but under pipelineMaxBodyBytes.
	payload := `{"x":"` + strings.Repeat("a", 2<<20) + `"}`
	r := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(payload)))
	w := httptest.NewRecorder()
	var dst map[string]any
	if !decodeCappedJSONLimit(w, r, &dst, pipelineMaxBodyBytes) {
		t.Fatalf("2 MiB body should decode under the pipeline cap; status=%d", w.Code)
	}

	// The same body must be rejected by the default 1 MiB cap.
	r2 := httptest.NewRequest("POST", "/x", bytes.NewReader([]byte(payload)))
	w2 := httptest.NewRecorder()
	var dst2 map[string]any
	if decodeCappedJSON(w2, r2, &dst2) || w2.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("2 MiB body should 413 under the default cap; ok=%v status=%d", w2.Code == 200, w2.Code)
	}
}

func TestDecodeCappedJSON_BadJSON_400(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	var dst map[string]any
	if decodeCappedJSON(w, r, &dst) {
		t.Fatal("bad JSON should not decode successfully")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestDecodeCappedJSON_Valid_OK(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"a":1}`))
	w := httptest.NewRecorder()
	var dst map[string]any
	if !decodeCappedJSON(w, r, &dst) {
		t.Fatalf("valid body should decode; status=%d", w.Code)
	}
	if dst["a"].(float64) != 1 {
		t.Errorf("dst not populated: %v", dst)
	}
}

func TestRejectInvalidField(t *testing.T) {
	// NUL byte → rejected (parity with intent).
	w := httptest.NewRecorder()
	if !rejectInvalidField(w, "abc\x00def", "credential_name", maxCredentialNameLength) {
		t.Error("NUL in field should be rejected")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	// Over length → rejected.
	w = httptest.NewRecorder()
	if !rejectInvalidField(w, strings.Repeat("x", maxEnvVarLength+1), "env_var", maxEnvVarLength) {
		t.Error("over-length field should be rejected")
	}
	// Normal value → accepted.
	w = httptest.NewRecorder()
	if rejectInvalidField(w, "ANTHROPIC_API_KEY", "env_var", maxEnvVarLength) {
		t.Error("normal value should be accepted")
	}
	// Empty value → accepted (presence checked separately).
	w = httptest.NewRecorder()
	if rejectInvalidField(w, "", "credential_name", maxCredentialNameLength) {
		t.Error("empty value should pass field validation")
	}
}
