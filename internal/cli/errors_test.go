package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeResponse builds an *http.Response with the given status and body,
// mirroring what CheckError receives from the API client.
func fakeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestCheckError_ReturnsTypedAPIError(t *testing.T) {
	err := CheckError(fakeResponse(404, `{"error":"agent not found"}`))
	if err == nil {
		t.Fatal("expected error for 404")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Status != 404 {
		t.Errorf("Status = %d, want 404", apiErr.Status)
	}
	if apiErr.Detail != "agent not found" {
		t.Errorf("Detail = %q, want %q", apiErr.Detail, "agent not found")
	}
	// The rendered message must stay byte-identical to the historical
	// format — scripts and the test harness match on it.
	if got, want := err.Error(), "API error (404): agent not found"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestCheckError_RFC7807DetailAndExtensions(t *testing.T) {
	body := `{"detail":"routine requires integrations","missing_integrations":["slack","github"]}`
	err := CheckError(fakeResponse(422, body))

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Status != 422 {
		t.Errorf("Status = %d, want 422", apiErr.Status)
	}
	want := "API error (422): routine requires integrations [connect: slack, github]"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
	// Extensions carry the full parsed body for machine consumers.
	raw, ok := apiErr.Extensions["missing_integrations"]
	if !ok {
		t.Fatalf("Extensions missing missing_integrations: %#v", apiErr.Extensions)
	}
	list, ok := raw.([]interface{})
	if !ok || len(list) != 2 {
		t.Errorf("missing_integrations = %#v, want 2 entries", raw)
	}
}

func TestCheckError_NonJSONBody(t *testing.T) {
	err := CheckError(fakeResponse(500, "internal server error"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if got, want := err.Error(), "API error (500): internal server error"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestCheckError_SuccessIsNil(t *testing.T) {
	for _, status := range []int{200, 201, 204, 299} {
		if err := CheckError(fakeResponse(status, "")); err != nil {
			t.Errorf("CheckError(%d) = %v, want nil", status, err)
		}
	}
}

func TestExitCodeFor_StatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   int
	}{
		{400, ExitValidation},
		{422, ExitValidation},
		{404, ExitNotFound},
		{401, ExitAuth},
		{403, ExitAuth},
		{409, ExitConflict},
		{429, ExitRateLimited},
		{500, ExitServer},
		{503, ExitServer},
		{418, ExitGeneric}, // unmapped 4xx falls back to generic
	}
	for _, tc := range cases {
		err := CheckError(fakeResponse(tc.status, `{"error":"x"}`))
		if got := ExitCodeFor(err); got != tc.want {
			t.Errorf("ExitCodeFor(status %d) = %d, want %d", tc.status, got, tc.want)
		}
	}
}

func TestExitCodeFor_PlainErrorIsGeneric(t *testing.T) {
	if got := ExitCodeFor(errors.New("boom")); got != ExitGeneric {
		t.Errorf("ExitCodeFor(plain) = %d, want %d", got, ExitGeneric)
	}
	if got := ExitCodeFor(nil); got != ExitOK {
		t.Errorf("ExitCodeFor(nil) = %d, want %d", got, ExitOK)
	}
}

func TestWithExitCode_WrapsAndUnwraps(t *testing.T) {
	base := errors.New("not logged in")
	err := WithExitCode(base, ExitAuth)
	if got := ExitCodeFor(err); got != ExitAuth {
		t.Errorf("ExitCodeFor = %d, want %d", got, ExitAuth)
	}
	if !errors.Is(err, base) {
		t.Error("wrapped error lost identity for errors.Is")
	}
	if err.Error() != "not logged in" {
		t.Errorf("Error() = %q, want passthrough", err.Error())
	}
	// Wrapping nil returns nil so call sites can wrap unconditionally.
	if WithExitCode(nil, ExitAuth) != nil {
		t.Error("WithExitCode(nil) should be nil")
	}
	// A wrap further out (fmt.Errorf %w) must still surface the code.
	outer := fmt.Errorf("context: %w", err)
	if got := ExitCodeFor(outer); got != ExitAuth {
		t.Errorf("ExitCodeFor(outer) = %d, want %d", got, ExitAuth)
	}
}

func TestConnectionError_ExitCode(t *testing.T) {
	inner := errors.New("dial tcp: connection refused")
	err := &ConnectionError{Err: inner}
	if got := ExitCodeFor(err); got != ExitConnection {
		t.Errorf("ExitCodeFor = %d, want %d", got, ExitConnection)
	}
	if !errors.Is(err, inner) {
		t.Error("ConnectionError must unwrap to inner error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("Error() = %q, want inner message included", err.Error())
	}
}

func TestServerMismatchError_ExitCode(t *testing.T) {
	err := &ServerMismatchError{TokenHost: "dev2", RequestHost: "evil"}
	if got := ExitCodeFor(err); got != ExitAuth {
		t.Errorf("ExitCodeFor = %d, want %d", got, ExitAuth)
	}
}

func TestNewErrorEnvelope_APIError(t *testing.T) {
	err := CheckError(fakeResponse(404, `{"error":"agent not found"}`))
	env := NewErrorEnvelope(err)

	data, jerr := json.Marshal(env)
	if jerr != nil {
		t.Fatalf("marshal envelope: %v", jerr)
	}
	var decoded struct {
		Error struct {
			Message  string `json:"message"`
			Status   int    `json:"status"`
			ExitCode int    `json:"exit_code"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal(data, &decoded); jerr != nil {
		t.Fatalf("unmarshal envelope: %v", jerr)
	}
	if decoded.Error.Status != 404 {
		t.Errorf("status = %d, want 404", decoded.Error.Status)
	}
	if decoded.Error.ExitCode != ExitNotFound {
		t.Errorf("exit_code = %d, want %d", decoded.Error.ExitCode, ExitNotFound)
	}
	if decoded.Error.Message != "API error (404): agent not found" {
		t.Errorf("message = %q", decoded.Error.Message)
	}
}

func TestNewErrorEnvelope_PlainError(t *testing.T) {
	env := NewErrorEnvelope(errors.New("boom"))
	data, _ := json.Marshal(env)
	s := string(data)
	if !strings.Contains(s, `"message":"boom"`) {
		t.Errorf("envelope = %s, want message boom", s)
	}
	if !strings.Contains(s, `"exit_code":1`) {
		t.Errorf("envelope = %s, want exit_code 1", s)
	}
	if strings.Contains(s, `"status"`) {
		t.Errorf("envelope = %s, plain error must omit status", s)
	}
}
