package main

// Seeding the mirrored "Template Crewship Docs" (seeddata.MirrorQakfDoc): a
// native Docs page, the Crewship kind a Claude Docs document maps to.
//
// The same contract as the rest of the showcase, through the routes the CLI
// uses (`page create --allow-empty`, `doc initialize`):
//   - fail-soft: a server without Docs, or a refusal, is one line and a count;
//   - idempotent by slug: a Page that already holds a document, or anything
//     else, is left untouched;
//   - self-completing: the empty Page an earlier run created before its
//     initialization was refused is initialized by the next run, and nothing
//     else is.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// mirrorQakfSchema is the rich Docs schema: code blocks, inline code and links.
const mirrorQakfSchema = 3

// mirrorQakfInitialize is the body `crewship doc initialize` sends.
type mirrorQakfInitialize struct {
	ID     string          `json:"id" yaml:"id"`
	Schema int             `json:"schema" yaml:"schema"`
	PageID string          `json:"page_id" yaml:"page_id"`
	Body   json.RawMessage `json:"body" yaml:"body"`
}

func mirrorQakfRequest(pageID string) (mirrorQakfInitialize, error) {
	item := seeddata.MirrorQakfDoc
	body := seeddata.ShowcaseFile(item.File)
	var root struct {
		Type    string            `json:"type" yaml:"type"`
		Content []json.RawMessage `json:"content" yaml:"content"`
	}
	if err := json.Unmarshal(body, &root); err != nil || root.Type != "doc" || len(root.Content) == 0 {
		return mirrorQakfInitialize{}, fmt.Errorf("%s is not a document tree", item.File)
	}
	return mirrorQakfInitialize{ID: item.OperationID, Schema: mirrorQakfSchema, PageID: pageID, Body: body}, nil
}

// seedShowcaseMirrorQakf creates and initializes the mirrored Docs page.
func seedShowcaseMirrorQakf(client *cli.Client, out io.Writer) showcaseOutcome {
	item := seeddata.MirrorQakfDoc
	raw, err := seedShowcaseGet(client, "/api/v1/documents/status")
	var status struct {
		Enabled bool `json:"enabled" yaml:"enabled"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &status)
	}
	if err != nil {
		fmt.Fprintf(out, "  = %s: skipped (Docs status check failed: %v)\n", item.Slug, err)
		return showcaseUnavailable
	}
	if !status.Enabled {
		fmt.Fprintf(out, "  = %s: skipped (Docs are not enabled on this server)\n", item.Slug)
		return showcaseUnavailable
	}

	page, err := seedShowcaseLookup(client, out, item.Slug)
	if err != nil {
		return showcaseUnavailable
	}
	success := showcaseCreated
	var pageID string
	if page != nil {
		// Only the shell an earlier run left behind is ours to fill; the
		// server initializes only an empty Page its caller owns.
		if !page.emptyShell() {
			return showcaseExists
		}
		raw, err := seedShowcaseGet(client, "/api/v1/pages/"+pagePathEscape(item.Slug))
		if pageID, err = mirrorQakfPageID(raw, err); err != nil {
			fmt.Fprintf(out, "  = %s: skipped (cannot read the empty Page: %v)\n", item.Slug, err)
			return showcaseUnavailable
		}
		success = showcaseCompleted
	} else {
		raw, status, err := seedShowcasePost(client, "/api/v1/pages", map[string]any{
			"slug": item.Slug, "name": item.Name, "panels": []any{}, "allow_empty": true,
		})
		if status == http.StatusConflict {
			return showcaseExists
		}
		if pageID, err = mirrorQakfPageID(raw, err); err != nil {
			fmt.Fprintf(out, "  = %s: skipped (page create: %v)\n", item.Slug, err)
			return showcaseUnavailable
		}
	}

	request, err := mirrorQakfRequest(pageID)
	if err == nil {
		_, _, err = seedShowcasePost(client, "/api/v1/pages/"+pagePathEscape(item.Slug)+"/document/initialize", request)
	}
	if err != nil {
		fmt.Fprintf(out, "  ! %s: incomplete (Docs refused initialization: %v); the empty Page is kept and the next seed initializes it\n", item.Slug, err)
		return showcaseIncomplete
	}
	return success
}

// mirrorQakfPageID reads the Page id a schema 3 initialize must name.
func mirrorQakfPageID(raw []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	var page struct {
		ID string `json:"id" yaml:"id"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || page.ID == "" {
		return "", fmt.Errorf("the answer names no Page id")
	}
	return page.ID, nil
}
