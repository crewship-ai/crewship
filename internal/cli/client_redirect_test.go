package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRefusesCrossOriginRedirect(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			leaked := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer source.Close()
			c := NewClient(source.URL, "test-secret", "")
			resp, err := c.Do(http.MethodPost, "/api/v1/example", strings.NewReader(`{"password":"body-secret"}`))
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || leaked {
				t.Fatalf("redirect error=%v target reached=%v", err, leaked)
			}
		})
	}
}

func TestClientSameOriginRedirect(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/second", http.StatusTemporaryRedirect)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("token not preserved")
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer s.Close()
	resp, err := NewClient(s.URL, "test-token", "").Get("/first")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestRedirectOriginPolicy(t *testing.T) {
	original, _ := http.NewRequestWithContext(context.Background(), "GET", "https://crewship.example/api", nil)
	for _, target := range []string{"http://crewship.example/api", "https://crewship.example:8443/api", "https://child.crewship.example/api", "https://user@crewship.example/api"} {
		req, _ := http.NewRequest("GET", target, nil)
		if err := sameOriginRedirect(req, []*http.Request{original}); err == nil {
			t.Errorf("accepted %s", target)
		}
	}
	chain := make([]*http.Request, 10)
	for i := range chain {
		chain[i] = original
	}
	if err := sameOriginRedirect(original, chain); err == nil {
		t.Fatal("accepted redirect loop")
	}
}
