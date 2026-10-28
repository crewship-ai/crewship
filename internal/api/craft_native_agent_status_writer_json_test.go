package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeAgentStatusWriterJSONOnlyWireHeaders(t *testing.T) {
	const raw = `<script>alert("native tool text")</script>`
	for _, explicit := range []bool{false, true} {
		name := "implicit200"
		if explicit {
			name = "explicit202-first-status"
		}
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer := &nativeAgentStatusWriter{ResponseWriter: recorder}
			wantStatus := http.StatusOK
			if explicit {
				wantStatus = http.StatusAccepted
				writer.WriteHeader(wantStatus)
				writer.WriteHeader(http.StatusTeapot)
			}
			if n, err := writer.Write([]byte(raw)); err != nil || n != len(raw) {
				t.Fatalf("write=%d,%v", n, err)
			}
			response := recorder.Result()
			defer response.Body.Close()
			if got := response.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("actual wire content-type=%q; raw HTML must never be sniffed", got)
			}
			if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("actual wire nosniff=%q", got)
			}
			if response.StatusCode != wantStatus || writer.status != wantStatus {
				t.Errorf("first actual status=%d tracked=%d want=%d", response.StatusCode, writer.status, wantStatus)
			}
			if got := recorder.Body.String(); got != raw {
				t.Errorf("body changed: %q", got)
			}
		})
	}
}
