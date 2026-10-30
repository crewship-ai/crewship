package cli

import (
	"errors"
	"testing"
)

// TestCheckError_PageRejection422 covers the pages 422 envelope
// (internal/api/pages_data.go pageRejection). Its "detail" is an object, not
// the RFC 7807 string CheckError also understands, so the message must come
// from "error" and the object must not make the whole body unreadable.
func TestCheckError_PageRejection422(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantDetail string
		wantError  string
	}{
		{
			name:       "page rejection with error sentence",
			body:       `{"rejected":true,"kind":"cap","detail":{"bytes_attempted":70000,"bytes_limit":65536},"message":"payload exceeds the 64 KiB limit","error":"payload exceeds the 64 KiB limit"}`,
			wantDetail: "payload exceeds the 64 KiB limit",
			wantError:  "API error (422): payload exceeds the 64 KiB limit",
		},
		{
			name:       "RFC 7807 string detail still read",
			body:       `{"title":"Unprocessable","status":422,"detail":"crew is not linked"}`,
			wantDetail: "crew is not linked",
			wantError:  "API error (422): crew is not linked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckError(fakeResponse(422, tc.body))
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("expected *APIError, got %T", err)
			}
			if apiErr.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", apiErr.Detail, tc.wantDetail)
			}
			if err.Error() != tc.wantError {
				t.Errorf("Error() = %q, want %q", err.Error(), tc.wantError)
			}
		})
	}

	// The structured fields stay available to --json consumers.
	err := CheckError(fakeResponse(422, cases[0].body))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Extensions["kind"] != "cap" {
		t.Errorf("Extensions[kind] = %v, want cap", apiErr.Extensions["kind"])
	}
	if _, ok := apiErr.Extensions["detail"].(map[string]interface{}); !ok {
		t.Errorf("Extensions[detail] = %#v, want the object", apiErr.Extensions["detail"])
	}
}
