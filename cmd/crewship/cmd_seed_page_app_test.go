package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real auth/API, SQLite and Git; optional real Docker compilation of the shipped
// React bundle. Never contacts dev3 or seeds unrelated workspace resources.
func TestSeedPageAppLifecycle(t *testing.T) {
	for _, mode := range []string{"disabled-worker", "publish", "custom-page"} {
		t.Run(mode, func(t *testing.T) {
			image := os.Getenv("PAGES_TEST_BUILD_IMAGE")
			if mode == "publish" && image == "" {
				// SKIP-WAIVER(#2472): pages-apps CI supplies the pinned image and requires the publish lifecycle subtest.
				t.Skip("set PAGES_TEST_BUILD_IMAGE for real Docker build")
			}
			db := testutil.MigratedDB(t).DB
			const ws = "cabcdefghijklmnopqrs"
			const token = "crewship_cli_pagesseed000000000000000000"
			for _, q := range []string{
				`INSERT INTO workspaces(id,name,slug) VALUES('cabcdefghijklmnopqrs','Pages','pages-seed')`,
				`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('pages-seed-crew','cabcdefghijklmnopqrs','Operations','ops','free')`,
				`INSERT INTO pipelines(id,workspace_id,slug,name,author_crew_id,definition_json,definition_hash,status) VALUES('pages-seed-routine','cabcdefghijklmnopqrs','pages-operations-sample','Operations sample','pages-seed-crew','{}','h','active')`,
				`INSERT INTO users(id,email,full_name) VALUES('pages-seed-owner','pages@example.invalid','Pages Owner')`,
				`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pages-seed-member','cabcdefghijklmnopqrs','pages-seed-owner','OWNER')`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('pages-seed-token','pages-seed-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
				t.Fatal(err)
			}
			opts := []api.RouterOption{api.WithPageProjectsPath(t.TempDir())}
			if mode == "publish" {
				opts = append(opts, api.WithPageBuildImage(image), api.WithPageRuntime("http://pages.example.net", "http://studio.example.com"))
			}
			router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), opts...)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(router)
			defer srv.Close()
			client := cli.NewClient(srv.URL, token, ws)
			var page seeddata.PageDef
			for _, p := range seeddata.Pages {
				if p.Slug == "custom-operations" {
					page = p
				}
			}
			if page.Project == nil {
				t.Fatal("missing custom demo")
			}
			seed := func() error {
				if err := seedOnePage(client, ws, page); err != nil {
					return err
				}
				return seedPageApp(context.Background(), client, page)
			}
			if mode == "custom-page" {
				custom := page
				custom.Name = "My own Operations"
				if err := seedOnePage(client, ws, custom); err != nil {
					t.Fatal(err)
				}
				if err := seed(); err != nil {
					t.Fatal(err)
				}
				var n int
				if err := db.QueryRow("SELECT count(*) FROM page_project_revisions").Scan(&n); err != nil || n != 0 {
					t.Fatalf("custom Page replaced: %d %v", n, err)
				}
				var name string
				if err := db.QueryRow("SELECT name FROM pages WHERE slug='custom-operations'").Scan(&name); err != nil || name != custom.Name {
					t.Fatalf("custom Page changed: %s %v", name, err)
				}
				return
			}
			err = seed()
			if mode == "disabled-worker" {
				if err == nil || !strings.Contains(err.Error(), "worker") {
					t.Fatalf("expected clear disabled worker error: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			count := func(table string) int {
				t.Helper()
				var n int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if count("pages") != 1 || count("page_project_revisions") != 1 {
				t.Fatal("seed did not persist page + Git source")
			}
			for i := 0; i < 2; i++ {
				err = seed()
				if mode == "publish" && err != nil {
					t.Fatal(err)
				}
			}
			if count("page_project_revisions") != 1 {
				t.Fatal("reseed generated duplicate revisions")
			}
			if mode == "publish" {
				if count("page_project_builds") != 1 || count("page_project_publications") != 1 {
					t.Fatal("reseed generated duplicate builds/publications")
				}
				resp, err := client.Post("/api/v1/pages/custom-operations/project/unpublish", map[string]any{"expected_publication": 1})
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("unpublish %d", resp.StatusCode)
				}
				if err = seed(); err != nil {
					t.Fatal(err)
				}
				resp, err = client.Get("/api/v1/pages/custom-operations/application")
				if err != nil {
					t.Fatal(err)
				}
				var app struct {
					Publication any `json:"publication"`
					Version     int `json:"publication_version"`
				}
				err = json.NewDecoder(resp.Body).Decode(&app)
				resp.Body.Close()
				if err != nil || app.Publication != nil || app.Version != 1 {
					t.Fatalf("withdrawal lost: %+v %v", app, err)
				}
			}
			// A modified source must survive even while unpublished / without a worker.
			resp, err := client.Get("/api/v1/pages/custom-operations/project")
			if err != nil {
				t.Fatal(err)
			}
			var draft struct {
				Project *pages.SourceProject `json:"project"`
			}
			err = json.NewDecoder(resp.Body).Decode(&draft)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			// Add a comment to existing TSX rather than alter project grammar.
			for i := range draft.Project.Files {
				if draft.Project.Files[i].Path == "src/main.tsx" {
					draft.Project.Files[i].Content += "\n// user edit\n"
				}
			}
			resp, err = client.Put("/api/v1/pages/custom-operations/project", map[string]any{"expected_revision": 1, "project": draft.Project})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("edit %d", resp.StatusCode)
			}
			if err = seed(); err != nil {
				t.Fatal(err)
			}
			if count("page_project_revisions") != 2 {
				t.Fatal("user source overwritten")
			}
		})
	}
}
