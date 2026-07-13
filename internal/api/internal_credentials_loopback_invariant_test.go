package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequestIsLoopback_InvariantIgnoresForwardedHeaders is the load-bearing
// invariant behind the include_values=true plaintext credential readback
// (#1083): requestIsLoopback must decide SOLELY on the transport-level
// RemoteAddr and never trust caller-supplied X-Forwarded-For / X-Real-IP.
//
// If a future change wires XFF resolution into this function, a proxied
// non-loopback caller could spoof `X-Forwarded-For: 127.0.0.1` and unlock
// plaintext readback. This test pins that it cannot.
func TestRequestIsLoopback_InvariantIgnoresForwardedHeaders(t *testing.T) {
	t.Run("loopback RemoteAddr → true", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.RemoteAddr = "127.0.0.1:5555"
		if !requestIsLoopback(r) {
			t.Fatal("loopback RemoteAddr must be treated as loopback")
		}
	})

	t.Run("non-loopback RemoteAddr with spoofed forwarded headers → false", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.RemoteAddr = "203.0.113.7:44321" // public address
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		r.Header.Set("X-Real-IP", "127.0.0.1")
		if requestIsLoopback(r) {
			t.Fatal("forwarded headers must NOT make a non-loopback caller look like loopback")
		}
	})
}
