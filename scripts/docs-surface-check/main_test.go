package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// The deployed-site comparison must be opt-in. Without this, the check reached
// docs.crewship.ai on every pull request and failed whenever the local
// navigation declared a page Mintlify had not published yet — which is the
// state of every PR that adds documentation.
func TestServedCheckIsSkippedWithoutURL(t *testing.T) {
	served, err := checkServed("", 279)
	if err != nil {
		t.Fatalf("checkServed(\"\", …) = %v; an unset URL must skip the deployed comparison, not fail", err)
	}
	if served != -1 {
		t.Errorf("served = %d, want -1 to mark the comparison as not run", served)
	}
}

func TestServedCheckMakesNoRequestWithoutURL(t *testing.T) {
	reached := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer srv.Close()

	if _, err := checkServed("", 1); err != nil {
		t.Fatal(err)
	}
	if reached {
		t.Fatal("checkServed made an HTTP request with no URL configured; the repository-side gate must stay hermetic")
	}
}

// navigationPages must read the structure, not guess which strings look like a
// page. The predicate it replaced required a "/" or one of a few known
// prefixes, so `philosophy`, `production-checklist` and `architecture` — all
// declared at the top level of the real docs.json — were never counted and
// therefore never checked for existence. The count went 279 → 282 on the real
// file when this was fixed.
func TestNavigationPagesReadsStructureNotStringShape(t *testing.T) {
	nav := []byte(`{
	  "tabs": [
	    {
	      "tab": "Docs",
	      "icon": "book-open",
	      "groups": [
	        {
	          "group": "Get Started",
	          "pages": ["index", "quickstart", "philosophy", "production-checklist", "architecture"]
	        },
	        {
	          "group": "Guides",
	          "pages": [
	            "guides/first-projects",
	            {"group": "Nested", "pages": ["guides/deep/one"]}
	          ]
	        }
	      ]
	    }
	  ]
	}`)

	got := navigationPages(nav)
	want := []string{
		"architecture", "guides/deep/one", "guides/first-projects", "index",
		"philosophy", "production-checklist", "quickstart",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("navigationPages() = %v\nwant %v", got, want)
	}
	// Group labels, tab names and icons live outside `pages` and must not be
	// mistaken for page ids — that is the other half of a shape-based guess.
	for _, notAPage := range []string{"Docs", "book-open", "Get Started", "Guides", "Nested"} {
		if slices.Contains(got, notAPage) {
			t.Errorf("%q is navigation chrome, not a page id, but it was collected", notAPage)
		}
	}
}

// With a URL it still has to do its job: a deployed index that is behind the
// checkout is real drift, and the message must say which side is short.
func TestServedCheckReportsDeployedDrift(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/llms.txt") {
			_, _ = w.Write([]byte("# Docs\n\n- [One](https://example.test/one)\n- [Two](https://example.test/two)\n"))
			return
		}
		_, _ = w.Write([]byte("full index body"))
	}))
	defer srv.Close()

	if _, err := checkServed(srv.URL, 2); err != nil {
		t.Fatalf("checkServed with a caught-up index = %v, want nil", err)
	}

	served, err := checkServed(srv.URL, 5)
	if err == nil {
		t.Fatal("checkServed with a lagging index = nil, want an error")
	}
	if served != 2 {
		t.Errorf("served = %d, want 2", served)
	}
	if !strings.Contains(err.Error(), "lists 2 pages") || !strings.Contains(err.Error(), "declares 5") {
		t.Errorf("error does not name both counts: %v", err)
	}
}
