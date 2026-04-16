package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// mustUnmarshal decodes rr.Body into out, failing the test fast on decode errors
// so misleading downstream zero-value assertions don't mask handler shape regressions.
func mustUnmarshal[T any](t *testing.T, rr *httptest.ResponseRecorder, out *T) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), out); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, rr.Body.String())
	}
}
