package main

// `crewship page access <slug> --me` — the caller's own paths to a page
// (docs/prd/pages-folder-permissions-linux-model-2026-09-13.md §3/10, §6;
// #2533).
//
//	GET /api/v1/pages/{slug}/access/me   page access <slug> --me
//
// It prints the server's paths verbatim — `owner`, `role`, `crew:<slug>`,
// `panel_crew:<slug>`, `folder:<slug>`, `grant` — the same words the page
// list's REACH column uses, because the vocabulary is the API's and a client
// that reworded it would be a second place for it to drift. Anyone who can
// open the page may ask; the answer describes them and nobody else. Who
// ELSE reaches a page is the owner's and the admin's question, answered by
// the folder's `page folder acl` and the page's `page grants`.

import (
	"encoding/json"
	"fmt"
	"strings"
)

type pageAccessMeJSON struct {
	Page        string   `json:"page"`
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Label       string   `json:"label"`
	Paths       []string `json:"paths"`
	Folder      string   `json:"folder"`
	Shared      string   `json:"shared"`
}

// runPageAccessMe answers `page access <slug> --me`: the caller's own paths
// to the page, in the API's words (owner, role, crew:<slug>,
// panel_crew:<slug>, folder:<slug>, grant). When the page is in a folder its
// slug and sharing label are printed too — the label, never the names: who a
// folder is shared with is its managers' to read ("page folder acl").
func runPageAccessMe(slug string) error {
	client, err := pageClient()
	if err != nil {
		return err
	}
	raw, err := pageFolderGet(client, "/api/v1/pages/"+pagePathEscape(slug)+"/access/me")
	if err != nil {
		return err
	}
	f := newFormatter()
	switch f.Format {
	case "json", "yaml", "ndjson":
		return pageEmitMachine(f, raw, "{}")
	}
	var doc pageAccessMeJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if f.Format == "quiet" {
		for _, p := range doc.Paths {
			fmt.Println(p)
		}
		return nil
	}
	fmt.Printf("%s reaches page %s by: %s\n", doc.Label, doc.Page, strings.Join(doc.Paths, ", "))
	if doc.Folder != "" {
		fmt.Printf("In folder %s (shared: %s).\n", doc.Folder, doc.Shared)
	}
	return nil
}
