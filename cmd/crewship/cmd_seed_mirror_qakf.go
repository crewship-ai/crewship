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
//
// The original's comment threads follow through `doc comment`, each with a
// fixed id. A thread is added only where the document still holds its quoted
// text at the same place, so a later run completes the threads a refusal
// left out and never comments on anybody's edited content.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"unicode/utf16"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// mirrorQakfSchema is the formatted Docs schema: marks, code languages,
// quotes and check lists on top of schema 3's code blocks, code and links.
const mirrorQakfSchema = 4

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
			if !page.HasDocument {
				return showcaseExists
			}
			added, err := seedMirrorQakfThreads(client, item.Slug)
			if err != nil {
				fmt.Fprintf(out, "  ! %s: comment threads incomplete (%v); the next seed adds them\n", item.Slug, err)
				return showcaseIncomplete
			}
			if added > 0 {
				return showcaseCompleted
			}
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
	if _, err := seedMirrorQakfThreads(client, item.Slug); err != nil {
		fmt.Fprintf(out, "  ! %s: comment threads incomplete (%v); the next seed adds them\n", item.Slug, err)
		return showcaseIncomplete
	}
	return success
}

// mirrorQakfThread is one entry of threads.json.
type mirrorQakfThread struct {
	ID    string `json:"id" yaml:"id"`
	Block string `json:"block" yaml:"block"`
	From  int    `json:"from" yaml:"from"`
	To    int    `json:"to" yaml:"to"`
	Quote string `json:"quote" yaml:"quote"`
	Text  string `json:"text" yaml:"text"`
}

func mirrorQakfThreads() ([]mirrorQakfThread, error) {
	var threads []mirrorQakfThread
	if err := json.Unmarshal(seeddata.ShowcaseFile(seeddata.MirrorQakfDoc.Threads), &threads); err != nil {
		return nil, fmt.Errorf("%s: %w", seeddata.MirrorQakfDoc.Threads, err)
	}
	return threads, nil
}

// seedMirrorQakfThreads adds the threads the document does not hold yet and
// returns how many it added. A document on another schema, or one whose
// quoted text moved, gets none: the mirror's anchors describe this copy only.
func seedMirrorQakfThreads(client *cli.Client, slug string) (int, error) {
	threads, err := mirrorQakfThreads()
	if err != nil {
		return 0, err
	}
	base := "/api/v1/pages/" + pagePathEscape(slug) + "/document"
	raw, err := seedShowcaseGet(client, base)
	var doc struct {
		Schema     int    `json:"schema" yaml:"schema"`
		Rev        int64  `json:"rev" yaml:"rev"`
		Epoch      string `json:"epoch" yaml:"epoch"`
		PageID     string `json:"page_id" yaml:"page_id"`
		CanComment bool   `json:"can_comment" yaml:"can_comment"`
		Outline    []struct {
			ID   string `json:"id" yaml:"id"`
			Text string `json:"text" yaml:"text"`
		} `json:"outline" yaml:"outline"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	if err != nil {
		return 0, fmt.Errorf("document read: %w", err)
	}
	if doc.Schema != mirrorQakfSchema || !doc.CanComment {
		return 0, nil
	}
	raw, err = seedShowcaseGet(client, base+"/comments")
	var listing struct {
		Comments []struct {
			ID string `json:"id" yaml:"id"`
		} `json:"comments" yaml:"comments"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &listing)
	}
	if err != nil {
		return 0, fmt.Errorf("comment listing: %w", err)
	}
	present := map[string]bool{}
	for _, c := range listing.Comments {
		present[c.ID] = true
	}
	texts := map[string][]uint16{}
	for _, b := range doc.Outline {
		texts[b.ID] = utf16.Encode([]rune(b.Text))
	}
	added := 0
	for _, t := range threads {
		units, ok := texts[t.Block]
		if present[t.ID] || !ok || t.From < 0 || t.To <= t.From || t.To > len(units) || string(utf16.Decode(units[t.From:t.To])) != t.Quote {
			continue
		}
		body := map[string]any{"id": t.ID, "schema": doc.Schema, "page_id": doc.PageID, "epoch": doc.Epoch, "ifRev": doc.Rev,
			"block": t.Block, "from": t.From, "to": t.To, "text": t.Text}
		if _, _, err := seedShowcasePost(client, base+"/comments", body); err != nil {
			return added, fmt.Errorf("thread %s: %w", t.ID, err)
		}
		added++
	}
	return added, nil
}

// mirrorQakfPageID reads the Page id a schema 3 or 4 initialize must name.
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
