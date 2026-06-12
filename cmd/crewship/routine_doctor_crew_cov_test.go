package main

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// covGetterFunc adapts a plain func to the doctorHTTPGetter interface
// so each branch of checkAuthorCrew can be exercised without a server.
type covGetterFunc func(path string) (*http.Response, error)

func (f covGetterFunc) Get(path string) (*http.Response, error) { return f(path) }

func covHTTPResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func TestCheckAuthorCrew_Branches(t *testing.T) {
	t.Parallel()

	const crew = "crew-1"
	tests := []struct {
		name        string
		crewID      string
		getter      covGetterFunc
		wantLevel   doctorLevel
		wantMessage string // substring
		wantHint    string // substring, "" = don't check
	}{
		{
			name:        "empty crew id",
			crewID:      "",
			getter:      func(string) (*http.Response, error) { t.Fatal("must not call API"); return nil, nil },
			wantLevel:   doctorFail,
			wantMessage: "author_crew_id is empty",
			wantHint:    "--author-crew",
		},
		{
			name:        "transport error",
			crewID:      crew,
			getter:      func(string) (*http.Response, error) { return nil, errors.New("conn refused") },
			wantLevel:   doctorWarn,
			wantMessage: "could not query crew provisioning status",
		},
		{
			name:        "nil response without error",
			crewID:      crew,
			getter:      func(string) (*http.Response, error) { return nil, nil },
			wantLevel:   doctorWarn,
			wantMessage: "could not query crew provisioning status",
		},
		{
			name:        "404 crew gone",
			crewID:      crew,
			getter:      func(string) (*http.Response, error) { return covHTTPResp(404, `{}`), nil },
			wantLevel:   doctorFail,
			wantMessage: "author crew not found",
			wantHint:    "re-author",
		},
		{
			name:        "unexpected status",
			crewID:      crew,
			getter:      func(string) (*http.Response, error) { return covHTTPResp(503, `{}`), nil },
			wantLevel:   doctorWarn,
			wantMessage: "HTTP 503",
		},
		{
			name:        "decode failure",
			crewID:      crew,
			getter:      func(string) (*http.Response, error) { return covHTTPResp(200, `{not-json`), nil },
			wantLevel:   doctorWarn,
			wantMessage: "could not decode crew status response",
		},
		{
			name:   "no devcontainer config",
			crewID: crew,
			getter: func(string) (*http.Response, error) {
				return covHTTPResp(200, `{"status":"completed","devcontainer_config":"","cached_image":"img"}`), nil
			},
			wantLevel:   doctorWarn,
			wantMessage: "no devcontainer config",
			wantHint:    "devcontainer",
		},
		{
			name:   "completed",
			crewID: crew,
			getter: func(string) (*http.Response, error) {
				return covHTTPResp(200, `{"status":"completed","devcontainer_config":"{}","cached_image":"crewship-cached-image-very-long"}`), nil
			},
			wantLevel:   doctorOK,
			wantMessage: "provisioned (image cached:",
		},
		{
			name:   "in progress",
			crewID: crew,
			getter: func(string) (*http.Response, error) {
				return covHTTPResp(200, `{"status":"in_progress","devcontainer_config":"{}"}`), nil
			},
			wantLevel:   doctorWarn,
			wantMessage: "provisioning in progress",
			wantHint:    "crew provision status " + crew,
		},
		{
			name:   "failed",
			crewID: crew,
			getter: func(string) (*http.Response, error) {
				return covHTTPResp(200, `{"status":"failed","devcontainer_config":"{}"}`), nil
			},
			wantLevel:   doctorFail,
			wantMessage: "crew provisioning failed",
			wantHint:    "crew provision start " + crew,
		},
		{
			name:   "unknown status falls through",
			crewID: crew,
			getter: func(string) (*http.Response, error) {
				return covHTTPResp(200, `{"status":"queued","devcontainer_config":"{}"}`), nil
			},
			wantLevel:   doctorWarn,
			wantMessage: "provisioning status: queued",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := checkAuthorCrew(tt.getter, tt.crewID)
			if got.Name != "author_crew" {
				t.Errorf("Name = %q, want author_crew", got.Name)
			}
			if got.Level != tt.wantLevel {
				t.Errorf("Level = %q, want %q (msg: %s)", got.Level, tt.wantLevel, got.Message)
			}
			if !strings.Contains(got.Message, tt.wantMessage) {
				t.Errorf("Message = %q, want substring %q", got.Message, tt.wantMessage)
			}
			if tt.wantHint != "" && !strings.Contains(got.Hint, tt.wantHint) {
				t.Errorf("Hint = %q, want substring %q", got.Hint, tt.wantHint)
			}
		})
	}
}

func TestCheckAuthorCrew_RequestPathEscapesCrewID(t *testing.T) {
	t.Parallel()

	var gotPath string
	getter := covGetterFunc(func(path string) (*http.Response, error) {
		gotPath = path
		return covHTTPResp(200, `{"status":"completed","devcontainer_config":"{}"}`), nil
	})
	_ = checkAuthorCrew(getter, "crew/../etc")
	if !strings.Contains(gotPath, "/api/v1/crews/crew%2F..%2Fetc/provision") {
		t.Errorf("crew id not path-escaped: %q", gotPath)
	}
}
