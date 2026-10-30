package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// proxy502HTML mimics what nginx/Caddy/Cloudflare serve when the backend
// behind them is down: a full HTML page, not an API error body.
const proxy502HTML = `<!DOCTYPE html>
<html>
<head><title>502 Bad Gateway</title></head>
<body>
<center><h1>502 Bad Gateway</h1></center>
<hr><center>nginx/1.24.0</center>
</body>
</html>
`

// TestCheckError_NonJSONErrorBody drives CheckError through a real HTTP
// round trip so Content-Type and body framing match what the CLI sees in
// the field. HTML pages must be summarised (no markup in the message or
// the --format json detail); short plain text stays verbatim; the
// "API error (NNN): " prefix and exit-code mapping never change.
func TestCheckError_NonJSONErrorBody(t *testing.T) {
	longText := strings.Repeat("stack frame line that goes on and on\n", 40)

	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantDetail  string
		wantExit    int
	}{
		{
			name:        "proxy 502 html page",
			status:      http.StatusBadGateway,
			contentType: "text/html; charset=utf-8",
			body:        proxy502HTML,
			wantDetail:  "Bad Gateway (the server returned an HTML page instead of an API response; check the server URL and that the Crewship backend is running)",
			wantExit:    ExitServer,
		},
		{
			name:        "html without content type",
			status:      http.StatusServiceUnavailable,
			contentType: "application/octet-stream",
			body:        "<html><body>maintenance</body></html>",
			wantDetail:  "Service Unavailable (the server returned an HTML page instead of an API response; check the server URL and that the Crewship backend is running)",
			wantExit:    ExitServer,
		},
		{
			name:        "short plain text 500",
			status:      http.StatusInternalServerError,
			contentType: "text/plain; charset=utf-8",
			body:        "database is locked\n",
			wantDetail:  "database is locked",
			wantExit:    ExitServer,
		},
		{
			name:        "long plain text is not dumped",
			status:      http.StatusInternalServerError,
			contentType: "text/plain",
			body:        longText,
			wantDetail:  "Internal Server Error (non-JSON response body omitted)",
			wantExit:    ExitServer,
		},
		{
			name:        "empty body falls back to status text",
			status:      http.StatusNotFound,
			contentType: "text/plain",
			body:        "",
			wantDetail:  "Not Found",
			wantExit:    ExitNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			resp, err := http.Get(srv.URL)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			err = CheckError(resp)

			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("CheckError = %T %v, want *APIError", err, err)
			}
			if apiErr.Status != tc.status {
				t.Errorf("Status = %d, want %d", apiErr.Status, tc.status)
			}
			if got := ExitCodeFor(err); got != tc.wantExit {
				t.Errorf("exit code = %d, want %d", got, tc.wantExit)
			}
			wantMsg := "API error (" + strconv.Itoa(tc.status) + "): " + tc.wantDetail
			if got := err.Error(); got != wantMsg {
				t.Errorf("message = %q\n want %q", got, wantMsg)
			}
			if apiErr.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", apiErr.Detail, tc.wantDetail)
			}

			env := NewErrorEnvelope(err)
			for label, s := range map[string]string{"message": env.Error.Message, "detail": env.Error.Detail} {
				lower := strings.ToLower(s)
				if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") || strings.Contains(s, "\n") {
					t.Errorf("envelope %s leaks raw body: %q", label, s)
				}
				if len(s) > 400 {
					t.Errorf("envelope %s is %d bytes, want a bounded summary", label, len(s))
				}
			}
			if env.Error.Detail != tc.wantDetail {
				t.Errorf("envelope detail = %q, want %q", env.Error.Detail, tc.wantDetail)
			}
			if env.Error.ExitCode != tc.wantExit {
				t.Errorf("envelope exit_code = %d, want %d", env.Error.ExitCode, tc.wantExit)
			}
		})
	}
}

// TestCheckError_JSONWithoutErrorField_ErrorBody pins that a valid JSON
// body lacking error/detail is still passed through unchanged; the
// summary path is only for non-JSON bodies.
func TestCheckError_JSONWithoutErrorField_ErrorBody(t *testing.T) {
	err := CheckError(fakeResponse(409, `{"message":"version conflict"}`))
	if got, want := err.Error(), `API error (409): {"message":"version conflict"}`; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}
