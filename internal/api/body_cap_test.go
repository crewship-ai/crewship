package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBodyCap covers finding P3 (HIGH): the authed API router must bound the
// request body so an unbounded POST cannot OOM the process. The BodyCap
// middleware rejects an oversized Content-Length up front with 413 and backs
// that with a MaxBytesReader so a body that lies about (or omits) its length
// still cannot be read past the limit.
func TestBodyCap(t *testing.T) {
	const max = 1 << 10 // 1 KiB cap for the test

	t.Run("oversized content-length is rejected with 413", func(t *testing.T) {
		called := false
		h := BodyCap(max)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}))

		body := strings.Repeat("x", max+1)
		req := httptest.NewRequest("POST", "/api/v1/anything", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413; body=%s", rec.Code, rec.Body.String())
		}
		if called {
			t.Fatal("downstream handler must not run for an oversized body")
		}
	})

	t.Run("body within the cap passes through untouched", func(t *testing.T) {
		want := strings.Repeat("y", max-1)
		var got string
		h := BodyCap(max)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read body: %v", err)
			}
			got = string(b)
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("POST", "/api/v1/anything", strings.NewReader(want))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got != want {
			t.Fatalf("handler saw %d bytes, want %d", len(got), len(want))
		}
	})

	t.Run("oversized body with unknown length is bounded by the backstop", func(t *testing.T) {
		// ContentLength == -1 (unknown/chunked) bypasses the up-front
		// check, so the MaxBytesReader backstop must still stop the read
		// once it crosses the limit.
		var readErr error
		h := BodyCap(max)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, readErr = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest("POST", "/api/v1/anything", strings.NewReader(strings.Repeat("z", max*4)))
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if readErr == nil {
			t.Fatal("reading past the cap must error (MaxBytesReader backstop)")
		}
		if _, ok := readErr.(*http.MaxBytesError); !ok {
			t.Fatalf("read error = %T (%v); want *http.MaxBytesError", readErr, readErr)
		}
	})
}
