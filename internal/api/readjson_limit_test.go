package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jsonOfSize returns a valid JSON object padded with trailing whitespace to
// exactly n bytes, so the only thing that can make it fail is its size.
func jsonOfSize(n int) string {
	const obj = `{"name":"x"}`
	return obj + strings.Repeat(" ", n-len(obj))
}

// TestReadJSONBodyTooLarge pins the 1 MiB contract of readJSON: an oversized
// body is reported as errBodyTooLarge (never silently truncated and parsed or
// misreported as malformed), a body of exactly 1 MiB still decodes, and a
// malformed body stays an ordinary decode error.
func TestReadJSONBodyTooLarge(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantErr   bool
		wantLarge bool
	}{
		{"exactly 1 MiB decodes", jsonOfSize(1 << 20), false, false},
		{"1 MiB plus one byte is too large", jsonOfSize(1<<20 + 1), true, true},
		{"invalid JSON is a decode error", `{"name":`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.ContentLength = -1
			var got map[string]any
			err := readJSON(req, &got)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if errors.Is(err, errBodyTooLarge) != tc.wantLarge {
				t.Fatalf("errors.Is(err, errBodyTooLarge) = %v, want %v (err=%v)", !tc.wantLarge, tc.wantLarge, err)
			}
			if !tc.wantErr && got["name"] != "x" {
				t.Fatalf("decoded = %v", got)
			}
		})
	}
}

// TestReadBodyLimitMaxBytesReader: the global BodyCap middleware wraps the
// body in http.MaxBytesReader, whose overflow surfaces as *http.MaxBytesError.
// That is the same condition, so it must map to errBodyTooLarge as well.
func TestReadBodyLimitMaxBytesReader(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat(" ", 64)))
	req.Body = http.MaxBytesReader(rec, req.Body, 16)
	var v any
	if err := readJSON(req, &v); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("err = %v, want errBodyTooLarge", err)
	}
}

// TestReplyJSONBodyErrorStatus: the shared reply for a body read/decode
// failure answers 413 with a machine code for an oversized body and keeps the
// caller's 400 sentence for everything else.
func TestReplyJSONBodyErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		status   int
		code     string
		contains string
	}{
		{"too large", errBodyTooLarge, http.StatusRequestEntityTooLarge, errCodeBodyTooLarge, "too large"},
		{"wrapped too large", errors.Join(errors.New("ctx"), errBodyTooLarge), http.StatusRequestEntityTooLarge, errCodeBodyTooLarge, "too large"},
		{"malformed", io.ErrUnexpectedEOF, http.StatusBadRequest, "", "Invalid JSON body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			replyJSONBodyError(rec, tc.err, "Invalid JSON body")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body %q: %v", rec.Body.String(), err)
			}
			if body["code"] != tc.code || !strings.Contains(body["error"], tc.contains) {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

// TestCrewConfigBodyTooLarge: the 8 MiB crew-configuration reader reports an
// oversized body as errBodyTooLarge so create/update answer 413.
func TestCrewConfigBodyTooLarge(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(jsonOfSize(8<<20+1)))
	req.ContentLength = -1
	var v map[string]any
	if err := readCrewConfigJSON(req, &v); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("err = %v, want errBodyTooLarge", err)
	}
}

// TestInstanceBackupPlanBodyTooLarge drives the real route: a backup plan
// body over 1 MiB is refused with 413 and the body_too_large code instead of
// a 400 "invalid request body".
func TestInstanceBackupPlanBodyTooLarge(t *testing.T) {
	f := newInstanceFixture(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/admin/instance/backups/plans"},
		{"PUT", "/api/v1/admin/instance/backups/settings"},
	} {
		rr := f.do(f.boss, c.method, c.path, jsonOfSize(1<<20+1))
		wantCode(t, rr, http.StatusRequestEntityTooLarge, c.method+" "+c.path)
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["code"] != errCodeBodyTooLarge {
			t.Fatalf("%s %s body = %s (err %v)", c.method, c.path, rr.Body.String(), err)
		}
	}
}
