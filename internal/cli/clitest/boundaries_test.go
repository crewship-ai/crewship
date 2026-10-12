package clitest

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVerifyFallbackRequiresSingleCUIDResource(t *testing.T) {
	t.Parallel()
	server := NewStubServer()
	defer server.Close()
	const id = "c123456789012345678901"
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/v1/agents/" + id, 200},
		{"POST", "/api/v1/agents/" + id, 404},
		{"GET", "/api/v1/agents/" + id + "/debug", 404},
		{"GET", "/api/v1/agents/", 404},
		{"GET", "/api/v1/agents/cshort", 404},
		{"GET", "/api/v1/agents/z123456789012345678901", 404},
		{"GET", "/api/v1/agents/c12345678901234567890X", 404},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, server.URL()+tc.path, nil)
			require.NoError(t, err)
			response, err := server.Client().Do(req)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, tc.status, response.StatusCode)
			if tc.status == 200 {
				body, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				require.JSONEq(t, `{"id":"`+id+`"}`, string(body))
			}
		})
	}
	server.OnGet("/api/v1/agents/"+id, ErrorResponse(410, "deleted"))
	response, err := server.Client().Get(server.URL() + "/api/v1/agents/" + id)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 410, response.StatusCode, "explicit denial must override permissive test fallback")
}

func TestCustomFallbackAndMutationMethods(t *testing.T) {
	t.Parallel()
	server := NewStubServer()
	defer server.Close()
	server.SetFallback(TextResponse(418, "unexpected route"))
	server.OnPatch("/resource", TextResponse(200, "patched"))
	server.OnPut("/resource", TextResponse(201, "replaced"))
	for _, tc := range []struct {
		method, body string
		status       int
	}{{"PATCH", "patched", 200}, {"PUT", "replaced", 201}, {"GET", "unexpected route", 418}} {
		req, err := http.NewRequest(tc.method, server.URL()+"/resource", nil)
		require.NoError(t, err)
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(t, err)
		require.Equal(t, tc.status, response.StatusCode)
		require.Equal(t, tc.body, string(body))
		require.Equal(t, "text/plain; charset=utf-8", response.Header.Get("Content-Type"))
	}
}

type failedBody struct{}

func (failedBody) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }
func (failedBody) Close() error             { return nil }

func TestUnreadableBodyCannotBeRecordedOrDispatched(t *testing.T) {
	t.Parallel()
	server := NewStubServer()
	defer server.Close()
	called := false
	server.OnPost("/resource", func(*http.Request, []byte) (int, []byte, string) { called = true; return 200, nil, "" })
	request := httptest.NewRequest("POST", "/resource", nil)
	request.Body = failedBody{}
	response := httptest.NewRecorder()
	server.dispatch(response, request)
	require.Equal(t, 500, response.Code)
	require.Contains(t, response.Body.String(), "fixture read failure")
	require.False(t, called)
	require.Empty(t, server.Calls())
}

func TestJSONResponseRejectsInvalidFixture(t *testing.T) {
	require.PanicsWithValue(t, "clitest.JSONResponse: marshal: json: unsupported type: chan string", func() { JSONResponse(200, make(chan string)) })
}

func TestRecordedCallsCannotBeChangedThroughReturnedHeadersOrBody(t *testing.T) {
	t.Parallel()
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "all", true: "filtered"}[selected], func(t *testing.T) {
			server := NewStubServer()
			defer server.Close()
			server.OnPost("/resource", EmptyResponse(204))
			request, err := http.NewRequest("POST", server.URL()+"/resource", strings.NewReader("original"))
			require.NoError(t, err)
			request.Header.Set("X-Fixture", "original")
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			response.Body.Close()
			snapshot := server.Calls()
			if selected {
				snapshot = server.CallsFor("post", "/resource")
			}
			require.Len(t, snapshot, 1)
			snapshot[0].Headers.Set("X-Fixture", "changed")
			snapshot[0].Body[0] = 'X'
			recorded := server.Calls()
			require.Equal(t, "original", recorded[0].Headers.Get("X-Fixture"))
			require.Equal(t, "original", string(recorded[0].Body))
		})
	}
}
