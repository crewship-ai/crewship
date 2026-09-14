package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/pages"
)

// Only the reviewed built-in source is automatically published. A reseed never
// replaces a user draft, advances an existing publication, or undoes withdrawal.
func seedPageApp(ctx context.Context, client *cli.Client, page seeddata.PageDef) error {
	// The worker may use 120s (130s including host cleanup). Leave time for
	// setup, polling, candidate checks and publication; parent cancellation wins.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client = client.WithContext(ctx)
	base := "/api/v1/pages/" + url.PathEscape(page.Slug)
	request := func(method, path string, body, out any) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		resp, err := client.Do(method, base+path, body)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return resp.StatusCode, seedPageError(resp)
		}
		if out != nil {
			err = json.NewDecoder(io.LimitReader(resp.Body, pages.MaxTransferBytes)).Decode(out)
		}
		return resp.StatusCode, err
	}
	var publications struct {
		Version int64 `json:"publication_version"`
	}
	if _, err := request("GET", "/project/publications", nil, &publications); err != nil {
		return err
	}
	if publications.Version > 0 {
		fmt.Fprintf(os.Stderr, "  = app %s: publication history retained\n", page.Slug)
		return nil
	}
	// Convert the catalogue's duration strings to the authored document grammar.
	var raw []byte
	var err error
	// PagePanelDef has YAML tags only; use its validated API body for JSON fields.
	panels := make([]pages.PanelSpec, 0, len(page.Panels))
	for _, p := range page.Panels {
		body, err := seedPagePanelBody(p)
		if err != nil {
			return err
		}
		delete(body, "sla_seconds")
		body["sla"] = p.SLA
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
		var spec pages.PanelSpec
		if err = json.Unmarshal(raw, &spec); err != nil {
			return err
		}
		panels = append(panels, spec)
	}
	definition := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Name: page.Name, Slug: page.Slug, Description: page.Description}, Spec: pages.Spec{Panels: panels}}
	// A slug collision or a panel edit must not be overwritten by publication.
	// Read the authored wire shape, ignoring live measurements and ownership.
	var live pageWriteJSON
	if _, err = request("GET", "", nil, &live); err != nil {
		return err
	}
	live.Owner = ""
	if !reflect.DeepEqual(&live, pageWriteFrom(&definition)) {
		fmt.Fprintf(os.Stderr, "  = app %s: existing Page definition preserved\n", page.Slug)
		return nil
	}
	var draft struct {
		Revision   int64          `json:"revision"`
		Digest     string         `json:"digest"`
		Definition pages.Document `json:"definition"`
	}
	code, err := request("GET", "/project", nil, &draft)
	if code == http.StatusNotFound {
		_, err = request("PUT", "/project", map[string]any{"expected_revision": 0, "project": page.Project, "definition": definition}, &draft)
	}
	if err != nil {
		return err
	}
	digest, err := page.Project.Digest()
	if err != nil {
		return err
	}
	if draft.Digest != digest {
		fmt.Fprintf(os.Stderr, "  = app %s: custom source preserved\n", page.Slug)
		return nil
	}
	// PUT returns revision metadata only; read the stored definition before review.
	if _, err = request("GET", "/project", nil, &draft); err != nil {
		return err
	}
	if draft.Digest != digest || !reflect.DeepEqual(draft.Definition, definition) {
		fmt.Fprintf(os.Stderr, "  = app %s: custom source/definition preserved\n", page.Slug)
		return nil
	}
	type build struct {
		ID       string `json:"id"`
		Revision int64  `json:"source_revision"`
		Digest   string `json:"source_digest"`
		State    string `json:"state"`
	}
	var preview struct {
		Build *build `json:"build"`
	}
	if _, err = request("GET", "/project/preview", nil, &preview); err != nil {
		return err
	}
	if preview.Build == nil || preview.Build.Revision != draft.Revision || preview.Build.Digest != digest || (preview.Build.State != "ready" && preview.Build.State != "running") {
		var b build
		if _, err = request("POST", "/project/build", map[string]any{"expected_revision": draft.Revision}, &b); err != nil {
			return err
		}
		preview.Build = &b
	}
	for preview.Build.State == "running" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if _, err = request("GET", "/project/preview", nil, &preview); err != nil {
			return err
		}
		if preview.Build == nil {
			return fmt.Errorf("build disappeared")
		}
	}
	b := preview.Build
	if b.State != "ready" || b.Revision != draft.Revision || b.Digest != digest {
		return fmt.Errorf("demo build is not ready for the expected source (%s)", b.State)
	}
	if _, err = request("POST", "/project/check", map[string]any{"build_id": b.ID, "expected_revision": draft.Revision}, nil); err != nil {
		return err
	}
	// Publication is fenced on the definition and routine digests the review
	// snapshot reports right now. The seed is the same reviewer as anybody
	// else: it attests to values it read, not to values it assumed.
	var snapshot struct {
		Baseline struct {
			DefinitionDigest string `json:"definition_digest"`
		} `json:"baseline"`
		Routines []struct {
			Routine       string  `json:"routine"`
			CurrentDigest *string `json:"current_digest"`
		} `json:"routines"`
	}
	if _, err = request("GET", "/project/review", nil, &snapshot); err != nil {
		return err
	}
	current := map[string]string{}
	for _, row := range snapshot.Routines {
		if row.CurrentDigest != nil {
			current[row.Routine] = *row.CurrentDigest
		}
	}
	fenced := map[string]string{}
	for _, panel := range definition.Spec.Panels {
		for _, action := range panel.Actions {
			if action.Kind != pages.ActionCall || action.Routine == "" {
				continue
			}
			digest, ok := current[action.Routine]
			if !ok {
				return fmt.Errorf("demo Page %s calls routine %q, which has no current definition", page.Slug, action.Routine)
			}
			fenced[action.Routine] = digest
		}
	}
	_, err = request("POST", "/project/publish", map[string]any{"build_id": b.ID, "expected_revision": draft.Revision, "expected_publication": 0, "reviewed_code": true, "expected_definition_digest": snapshot.Baseline.DefinitionDigest, "expected_routine_digests": fenced}, nil)
	if err == nil {
		fmt.Fprintf(os.Stderr, "  + app %s: built, checked and published\n", page.Slug)
	}
	return err
}
