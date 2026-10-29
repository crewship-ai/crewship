package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPageArchiveCLINeverConfirmsWithoutReviewedToken(t *testing.T) {
	for _, operation := range []string{"archive", "unarchive"} {
		t.Run(operation, func(t *testing.T) {
			stub := pageStub(t)
			calls := 0
			stub.OnPost("/api/v1/pages/fleet-201/"+operation, func(_ *http.Request, _ []byte) (int, []byte, string) {
				calls++
				return 200, []byte(`{}`), "application/json"
			})
			if _, err := runPageCLI(t, "", "page", operation, "fleet-201", "--pages-version", "0"); err == nil {
				t.Fatal("unreviewed archive accepted")
			}
			if calls != 0 {
				t.Fatal("unreviewed action reached server")
			}
		})
	}
}

func TestPageArtifactArchiveCLIPreviewUsesExactFence(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/pages/fleet-201/archive", func(_ *http.Request, b []byte) (int, []byte, string) {
		var req struct {
			DryRun       bool  `json:"dry_run"`
			Confirm      bool  `json:"confirm"`
			PagesVersion int64 `json:"pages_version"`
		}
		if err := json.Unmarshal(b, &req); err != nil || !req.DryRun || req.Confirm || req.PagesVersion != 9 {
			t.Errorf("unexpected preview %s %v", b, err)
		}
		return 200, []byte(`{"dry_run":true,"review_token":"opaque","pages_version":9}`), "application/json"
	})
	if _, err := runPageCLI(t, "", "page", "artifact", "archive", "fleet-201", "--dry-run", "--pages-version", "9", "--format", "json"); err != nil {
		t.Fatal(err)
	}
}

func TestPageFolderAddCLIReviewDoesNotSilentlyRefreshFences(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/page-folders/engine/pages", func(_ *http.Request, b []byte) (int, []byte, string) {
		var req struct {
			Page         string `json:"page"`
			Confirm      bool   `json:"confirm"`
			PagesVersion int64  `json:"pages_version"`
			ACLVersion   int64  `json:"acl_version"`
			ReviewToken  string `json:"review_token"`
		}
		if err := json.Unmarshal(b, &req); err != nil || req.Page != "fleet-201" || !req.Confirm || req.PagesVersion != 9 || req.ACLVersion != 3 || req.ReviewToken != "opaque" {
			t.Errorf("unexpected confirmation %s %v", b, err)
		}
		return 409, []byte(`{"error":"review is stale; preview again"}`), "application/json"
	})
	if _, err := runPageCLI(t, "", "page", "folder", "add", "engine", "fleet-201", "--yes", "--pages-version", "9", "--acl-version", "3", "--review-token", "opaque"); err == nil {
		t.Fatal("stale review retried/accepted")
	}
}
