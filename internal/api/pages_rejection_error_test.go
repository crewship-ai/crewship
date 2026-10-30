package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWriteRejection_CarriesErrorSentence pins the 422 page rejection
// envelope to the shared error contract: generic clients (lib/api-error.ts
// apiErrorMessage, internal/cli CheckError) read a top-level "error" string,
// so without it a refused push surfaced as "Request failed (HTTP 422)" or raw
// JSON. The new field is additive; the fields producer scripts branch on
// (rejected, kind, detail, message) must stay exactly as before.
func TestWriteRejection_CarriesErrorSentence(t *testing.T) {
	cases := []struct {
		name string
		rej  pageRejection
	}{
		{
			name: "cap with detail",
			rej: pageRejection{
				Kind:    "cap",
				Message: "payload exceeds the 64 KiB limit",
				Detail:  map[string]any{"bytes_attempted": 70000, "bytes_limit": 65536},
			},
		},
		{
			name: "no message leaves error absent",
			rej:  pageRejection{Kind: "cap"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeRejection(rec, tc.rej)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422", rec.Code)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("body is not JSON: %v; body: %s", err, rec.Body.String())
			}

			if got["rejected"] != true {
				t.Errorf("rejected = %v, want true", got["rejected"])
			}
			if got["kind"] != tc.rej.Kind {
				t.Errorf("kind = %v, want %q", got["kind"], tc.rej.Kind)
			}
			if tc.rej.Message == "" {
				if _, ok := got["message"]; ok {
					t.Errorf("message present for empty Message: %v", got["message"])
				}
				if _, ok := got["error"]; ok {
					t.Errorf("error present for empty Message: %v", got["error"])
				}
				return
			}
			if got["message"] != tc.rej.Message {
				t.Errorf("message = %v, want %q", got["message"], tc.rej.Message)
			}
			if got["error"] != tc.rej.Message {
				t.Errorf("error = %v, want %q (same sentence as message)", got["error"], tc.rej.Message)
			}
			detail, ok := got["detail"].(map[string]any)
			if !ok {
				t.Fatalf("detail = %#v, want an object", got["detail"])
			}
			if detail["bytes_limit"] != float64(65536) || detail["bytes_attempted"] != float64(70000) {
				t.Errorf("detail = %v, want bytes_attempted:70000 bytes_limit:65536", detail)
			}
			for k := range got {
				switch k {
				case "rejected", "kind", "detail", "message", "error":
				default:
					t.Errorf("unexpected field %q in envelope", k)
				}
			}
		})
	}
}
