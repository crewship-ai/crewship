package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientInjectsWorkspaceID(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("workspace_id")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "my-workspace-cuid-12345678901234")
	resp, err := c.Get("/api/v1/agents")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	resp.Body.Close()

	if gotQuery != "my-workspace-cuid-12345678901234" {
		t.Errorf("workspace_id = %q, want my-workspace-cuid-12345678901234", gotQuery)
	}
}

func TestClientDoesNotOverrideExistingWorkspaceID(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("workspace_id")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "default-ws-cuid-1234567890123")
	resp, err := c.Get("/api/v1/agents?workspace_id=override-ws")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	resp.Body.Close()

	if gotQuery != "override-ws" {
		t.Errorf("workspace_id = %q, want override-ws", gotQuery)
	}
}

func TestClientSendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "my-secret-token", "")
	resp, err := c.Get("/test")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	resp.Body.Close()

	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("Authorization = %q, want Bearer my-secret-token", gotAuth)
	}
}

// TestClientTokenHostBinding covers the issue #571 guard: the bearer token
// must reach only the host it was issued for. httptest serves on 127.0.0.1,
// so a matching TokenHost lets the token through, a mismatched one refuses
// the request before it hits the network, and the override sends it anyway.
func TestClientTokenHostBinding(t *testing.T) {
	newServer := func(seen *string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*seen = r.Header.Get("Authorization")
			w.WriteHeader(200)
		}))
	}

	t.Run("matching host sends token", func(t *testing.T) {
		var gotAuth string
		srv := newServer(&gotAuth)
		defer srv.Close()
		c := NewClient(srv.URL, "secret", "")
		c.TokenHost = "127.0.0.1"
		resp, err := c.Get("/test")
		if err != nil {
			t.Fatalf("Get() error: %v", err)
		}
		resp.Body.Close()
		if gotAuth != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret", gotAuth)
		}
	})

	t.Run("mismatched host refuses without sending", func(t *testing.T) {
		var gotAuth string
		srv := newServer(&gotAuth)
		defer srv.Close()
		c := NewClient(srv.URL, "secret", "")
		c.TokenHost = "real-server.example.com"
		resp, err := c.Get("/test")
		if err == nil {
			if resp != nil {
				resp.Body.Close()
			}
			t.Fatal("expected ServerMismatchError, got nil")
		}
		var mm *ServerMismatchError
		if !errors.As(err, &mm) {
			t.Fatalf("error = %v, want *ServerMismatchError", err)
		}
		if gotAuth != "" {
			t.Errorf("token leaked to mismatched host: %q", gotAuth)
		}
	})

	t.Run("override sends to mismatched host", func(t *testing.T) {
		var gotAuth string
		srv := newServer(&gotAuth)
		defer srv.Close()
		c := NewClient(srv.URL, "secret", "")
		c.TokenHost = "real-server.example.com"
		c.AllowHostMismatch = true
		resp, err := c.Get("/test")
		if err != nil {
			t.Fatalf("Get() error: %v", err)
		}
		resp.Body.Close()
		if gotAuth != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret (override on)", gotAuth)
		}
	})

	t.Run("empty TokenHost disables the check", func(t *testing.T) {
		var gotAuth string
		srv := newServer(&gotAuth)
		defer srv.Close()
		c := NewClient(srv.URL, "secret", "") // TokenHost unset
		resp, err := c.Get("/test")
		if err != nil {
			t.Fatalf("Get() error: %v", err)
		}
		resp.Body.Close()
		if gotAuth != "Bearer secret" {
			t.Errorf("Authorization = %q, want Bearer secret (binding disabled)", gotAuth)
		}
	})
}

func TestClientPostJSON(t *testing.T) {
	var gotBody map[string]string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &gotBody)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "")
	body := map[string]string{"name": "test"}
	resp, err := c.Post("/api/v1/agents", body)
	if err != nil {
		t.Fatalf("Post() error: %v", err)
	}
	resp.Body.Close()

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody["name"] != "test" {
		t.Errorf("body.name = %q, want test", gotBody["name"])
	}
}

func TestClientHTTPMethods(t *testing.T) {
	methods := map[string]func(*Client, string) (*http.Response, error){
		"GET":    func(c *Client, p string) (*http.Response, error) { return c.Get(p) },
		"DELETE": func(c *Client, p string) (*http.Response, error) { return c.Delete(p) },
	}

	for method, fn := range methods {
		t.Run(method, func(t *testing.T) {
			var gotMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				w.WriteHeader(200)
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "", "")
			resp, err := fn(c, "/test")
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			resp.Body.Close()

			if gotMethod != method {
				t.Errorf("method = %q, want %q", gotMethod, method)
			}
		})
	}
}

func TestClientSlugResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "cuid_resolved_id_12345678", "slug": "my-slug"},
			})
			return
		}
		// For other requests, check that workspace_id is the resolved CUID
		w.Header().Set("X-WS-ID", r.URL.Query().Get("workspace_id"))
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "my-slug")
	resp, err := c.Get("/api/v1/agents")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	resp.Body.Close()

	wsID := resp.Header.Get("X-WS-ID")
	if wsID != "cuid_resolved_id_12345678" {
		t.Errorf("resolved workspace_id = %q, want cuid_resolved_id_12345678", wsID)
	}
}

func TestClientSlugResolutionCaching(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			callCount++
			json.NewEncoder(w).Encode([]map[string]string{
				{"id": "cuid_cached_id_1234567890", "slug": "cached"},
			})
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok", "cached")

	// Make two requests — workspace should only be resolved once
	resp1, _ := c.Get("/api/v1/agents")
	resp1.Body.Close()
	resp2, _ := c.Get("/api/v1/crews")
	resp2.Body.Close()

	if callCount != 1 {
		t.Errorf("workspace resolution called %d times, want 1 (should cache)", callCount)
	}
}

func TestReadJSON(t *testing.T) {
	body := `{"name":"test","count":42}`
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(body)),
	}

	var result struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	if err := ReadJSON(resp, &result); err != nil {
		t.Fatalf("ReadJSON() error: %v", err)
	}
	if result.Name != "test" || result.Count != 42 {
		t.Errorf("got %+v, want {test, 42}", result)
	}
}

func TestReadJSONInvalid(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("not json")),
	}
	var result struct{}
	if err := ReadJSON(resp, &result); err == nil {
		t.Error("ReadJSON() expected error for invalid JSON")
	}
}

func TestCheckErrorSuccess(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("")),
	}
	if err := CheckError(resp); err != nil {
		t.Errorf("CheckError(200) = %v, want nil", err)
	}
}

func TestCheckError4xx(t *testing.T) {
	tests := []struct {
		code int
		body string
		want string
	}{
		{400, `{"error":"bad request"}`, "bad request"},
		{403, `{"error":"forbidden"}`, "forbidden"},
		{404, `not found`, "not found"},
		{500, `{"error":"internal"}`, "internal"},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.code), func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.code,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}
			err := CheckError(resp)
			if err == nil {
				t.Fatal("CheckError() expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.want)
			}
		})
	}
}
