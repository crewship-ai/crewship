package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromptTemplateCommandsWire(t *testing.T) {
	guardCLIState(t)
	cases := []struct {
		name, method, path, input, expected string
		args                                []string
		status                              int
	}{
		{"list", "GET", "/api/v1/templates", "", "", []string{"list"}, 200},
		{"get", "GET", "/api/v1/templates/t1", "", "", []string{"get", "t1"}, 200},
		{"create", "POST", "/api/v1/templates", `{"name":"report","template_json":{"nested":true}}`, `"template_json":{"nested":true}`, []string{"create", "--file", "-"}, 201},
		{"update", "PATCH", "/api/v1/templates/t1", `{"name":"changed"}`, `"name":"changed"`, []string{"update", "t1", "--file", "-"}, 200},
		{"delete", "DELETE", "/api/v1/templates/t1", "", "", []string{"delete", "t1", "--yes"}, 204},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Errorf("got %s %s", r.Method, r.URL.Path)
				}
				raw, _ := io.ReadAll(r.Body)
				if tc.expected != "" && !strings.Contains(string(raw), tc.expected) {
					t.Errorf("body %s", raw)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status != 204 {
					_, _ = io.WriteString(w, `{"id":"t1"}`)
				}
			}))
			defer srv.Close()
			setStubCLI(t, srv.URL)
			c := newPromptTemplateCommand()
			c.SetArgs(tc.args)
			c.SetIn(strings.NewReader(tc.input))
			if err := c.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls %d", calls)
			}
		})
	}
}

func TestPromptTemplateRefusesInvalidInputs(t *testing.T) {
	guardCLIState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") }))
	defer srv.Close()
	setStubCLI(t, srv.URL)
	for _, args := range [][]string{{"delete", "t"}, {"get", ".."}, {"create", "--file", "-"}, {"update", "t", "--file", "-"}} {
		c := newPromptTemplateCommand()
		c.SilenceUsage = true
		c.SilenceErrors = true
		c.SetArgs(args)
		c.SetIn(strings.NewReader("[]"))
		if err := c.Execute(); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
