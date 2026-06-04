package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/recipes"
)

// Get resolves a single baked-in recipe by slug; unknown slugs 404.

func TestRecipeGet_Found(t *testing.T) {
	all := recipes.All()
	if len(all) == 0 {
		t.Skip("no baked-in recipes to assert against")
	}
	want := all[0]

	h := NewRecipeHandler(setupTestDB(t), newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/recipes/"+want.Slug, nil)
	req.SetPathValue("slug", want.Slug)
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got recipes.Recipe
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Slug != want.Slug {
		t.Errorf("slug=%q want %q", got.Slug, want.Slug)
	}
}

func TestRecipeGet_NotFound(t *testing.T) {
	h := NewRecipeHandler(setupTestDB(t), newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/recipes/does-not-exist", nil)
	req.SetPathValue("slug", "does-not-exist")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}
