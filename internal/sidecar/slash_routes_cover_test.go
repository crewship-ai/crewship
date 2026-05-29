package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSkillGenerate_NoIPC: missing IPC config → 503, mirroring the
// routine variant. Defensive — early init / test routers shouldn't
// crash.
func TestSkillGenerate_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/skills/generate", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.handleSkillGenerate(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestCredentialCreate_NoIPC: same defensive branch.
func TestCredentialCreate_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/credentials/create", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.handleCredentialCreate(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestCredentialRotate_NoIPC.
func TestCredentialRotate_NoIPC(t *testing.T) {
	srv := newQueryServer(t, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/credentials/abc/rotate", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.handleCredentialRotate(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
