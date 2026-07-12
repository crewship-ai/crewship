package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWorkspaceSlugDefinitiveMissFailsTyped locks the fix for the silent
// workspace-slug fallback: when the /workspaces preflight SUCCEEDS but the
// configured slug isn't in the list (a typo'd --workspace), the request must
// fail fast with a typed not-found error — not ride through with the raw slug
// as workspace_id and surface later as a confusing downstream 404.
func TestWorkspaceSlugDefinitiveMissFailsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"cabcdefghijklmnopqrst","slug":"alpha"}]`))
			return
		}
		t.Errorf("unexpected request to %s — a definitive slug miss must not reach the API", r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "beta") // "beta" is not in the workspace list
	resp, err := c.Get("/api/v1/agents")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error for unknown workspace slug, got nil")
	}
	var wsErr *WorkspaceNotFoundError
	if !errors.As(err, &wsErr) {
		t.Fatalf("expected *WorkspaceNotFoundError, got %T: %v", err, err)
	}
	if wsErr.Slug != "beta" {
		t.Errorf("Slug = %q, want %q", wsErr.Slug, "beta")
	}
	if code := ExitCodeFor(err); code != ExitNotFound {
		t.Errorf("ExitCodeFor = %d, want %d (ExitNotFound)", code, ExitNotFound)
	}
}

// TestWorkspaceSlugMissIsCached ensures a definitive miss doesn't re-issue the
// /workspaces preflight on every subsequent request.
func TestWorkspaceSlugMissIsCached(t *testing.T) {
	listCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			listCalls++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "ghost")
	for i := 0; i < 3; i++ {
		if resp, err := c.Get("/api/v1/agents"); err == nil {
			resp.Body.Close()
			t.Fatal("expected error for unknown workspace slug, got nil")
		}
	}
	if listCalls != 1 {
		t.Errorf("workspace list preflight ran %d times, want 1 (miss must be cached)", listCalls)
	}
}

// TestWorkspaceSlugPreflightErrorFallsBack preserves the deliberate fallback:
// when the preflight itself FAILS (server error, insufficient permissions on
// the list endpoint), the client keeps the historical behavior of passing the
// raw slug through — the real request then surfaces the real failure.
func TestWorkspaceSlugPreflightErrorFallsBack(t *testing.T) {
	var gotWorkspaceParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotWorkspaceParam = r.URL.Query().Get("workspace_id")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "beta")
	resp, err := c.Get("/api/v1/agents")
	if err != nil {
		t.Fatalf("expected fallback to raw slug on preflight failure, got error: %v", err)
	}
	resp.Body.Close()
	if gotWorkspaceParam != "beta" {
		t.Errorf("workspace_id param = %q, want raw slug %q", gotWorkspaceParam, "beta")
	}
}

// TestWorkspaceSlugResolvesToCUID confirms the happy path still resolves the
// slug to the CUID via the preflight and injects it as workspace_id.
func TestWorkspaceSlugResolvesToCUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the slug disk cache
	const cuid = "cabcdefghijklmnopqrst"
	var gotWorkspaceParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workspaces" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"id":"` + cuid + `","slug":"alpha"}]`))
			return
		}
		gotWorkspaceParam = r.URL.Query().Get("workspace_id")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "alpha")
	resp, err := c.Get("/api/v1/agents")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if gotWorkspaceParam != cuid {
		t.Errorf("workspace_id param = %q, want resolved CUID %q", gotWorkspaceParam, cuid)
	}
}
