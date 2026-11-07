package main

// Seeding the showcase: the content a demo is opened for rather than read
// about — native Word and Sheet documents, a Slides deck, a dashboard with
// data, two interactive artifacts and a Design canvas.
//
// Everything goes through the same REST routes the CLI commands use
// (`page create --allow-empty` + `craft initialize`, `page artifact create`,
// `page artifact data batch`, `page design create|apply|snapshot`,
// `feature-flag create|enable`), so a refusal here is the refusal a person
// would get.
//
// The phase is fail-soft per item. Each surface sits behind its own server
// capability (native Craft engines, artifact projects, Slides, interactive
// artifacts, dashboard data, Design), and a server without one is a normal
// configuration, not a broken seed. Such an item gets one line and is skipped;
// the rest of the showcase and the rest of the seed carry on. Only a cancelled
// context stops the phase.
//
// Idempotent by slug, and self-completing. An item whose Page already holds
// content is left untouched, so a re-seed never overwrites a document somebody
// has since edited. An item a previous run left INCOMPLETE — a Page whose
// native initialization was refused, a dashboard whose data was not written, a
// Design canvas whose layers were refused — is still empty in exactly the part
// that failed, and the next run fills that part and nothing else.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

// showcaseStats counts items by outcome. Every catalogue item lands in exactly
// one bucket.
type showcaseStats struct {
	// Created: made complete by this run. Completed: an incomplete item from an
	// earlier run, finished by this one. Skipped: already present and complete,
	// or holding content the seed did not write. Incomplete: created, but a
	// later step was refused; the next run retries that step. Unavailable: the
	// server cannot hold it right now; nothing was created.
	Created, Completed, Skipped, Incomplete, Unavailable int
}

var showcaseCraftLabel = map[string]string{"wordcraft": "native Word (WordCraft)", "gridcraft": "native Sheet (GridCraft)"}

func seedShowcase(ctx context.Context, client *cli.Client, out io.Writer) (showcaseStats, error) {
	var stats showcaseStats
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	client = client.WithContext(ctx)
	fmt.Fprintln(out, "Creating showcase documents and artifacts...")

	seedShowcaseFlags(client, out)

	available, statusErr := seedShowcaseCraftAvailability(client)
	for _, item := range seeddata.ShowcaseCrafts {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if statusErr != nil {
			fmt.Fprintf(out, "  = %s: skipped (native document status check failed: %v)\n", item.Slug, statusErr)
			stats.Unavailable++
			continue
		}
		if !available[item.Application] {
			fmt.Fprintf(out, "  = %s: skipped (%s is not available on this server)\n", item.Slug, showcaseCraftLabel[item.Application])
			stats.Unavailable++
			continue
		}
		seedShowcaseCount(&stats, out, item.Slug, seedShowcaseCraft(client, out, item))
	}
	for _, item := range seeddata.ShowcaseArtifacts {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		seedShowcaseCount(&stats, out, item.Slug, seedShowcaseArtifact(client, out, item))
	}
	for _, item := range seeddata.ShowcaseDesigns {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		seedShowcaseCount(&stats, out, item.Slug, seedShowcaseDesign(client, out, item))
	}

	fmt.Fprintf(out, "  showcase: %d created, %d completed, %d already present, %d incomplete (retried next seed), %d unavailable on this server\n",
		stats.Created, stats.Completed, stats.Skipped, stats.Incomplete, stats.Unavailable)
	return stats, nil
}

type showcaseOutcome int

const (
	showcaseCreated showcaseOutcome = iota
	showcaseCompleted
	showcaseExists
	showcaseIncomplete
	showcaseUnavailable
)

// seedShowcaseCount prints the success lines; refusals print their own line
// where the server's sentence is at hand.
func seedShowcaseCount(stats *showcaseStats, out io.Writer, slug string, o showcaseOutcome) {
	switch o {
	case showcaseCreated:
		fmt.Fprintf(out, "  + %s\n", slug)
		stats.Created++
	case showcaseCompleted:
		fmt.Fprintf(out, "  + %s: completed (an earlier seed left it incomplete)\n", slug)
		stats.Completed++
	case showcaseExists:
		fmt.Fprintf(out, "  = %s: already exists, left unchanged\n", slug)
		stats.Skipped++
	case showcaseIncomplete:
		stats.Incomplete++
	default:
		stats.Unavailable++
	}
}

// showcaseFlag is the part of GET /api/v1/feature-flags the seed reads; the
// list is resolved for the workspace on the request.
type showcaseFlag struct {
	Key             string `json:"key" yaml:"key"`
	Enabled         bool   `json:"enabled" yaml:"enabled"`
	OverrideEnabled *bool  `json:"override_enabled,omitempty" yaml:"override_enabled,omitempty"`
}

// seedShowcaseFlags turns on, for the seeded workspace only, the flags the
// showcase surfaces need. Every write is a per-workspace override
// (PUT …/override, which needs workspace OWNER/ADMIN) on the client's own
// workspace; nothing here changes an instance default. The one exception is a
// missing definition, created OFF instance-wide so it can be overridden — and
// that needs an instance administrator.
//
// A flag this workspace has explicitly switched off is left off: that override
// is somebody's decision. Each flag actually turned on gets its own line, and
// refusals are reported once; the items behind them then report their own.
func seedShowcaseFlags(client *cli.Client, out io.Writer) {
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		fmt.Fprintln(out, "  ! showcase feature flags: no workspace selected; none changed")
		return
	}
	current := map[string]showcaseFlag{}
	if resp, err := client.Get("/api/v1/feature-flags"); err == nil {
		var list []showcaseFlag
		if resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list) == nil {
			for _, f := range list {
				current[f.Key] = f
			}
		}
		_ = resp.Body.Close()
	}
	for _, key := range seeddata.ShowcaseFeatureFlagsCreateFirst {
		if _, defined := current[key]; defined {
			continue
		}
		resp, err := client.Post("/api/v1/feature-flags", map[string]any{"key": key, "enabled": false, "percentage": 0})
		if err == nil {
			_ = resp.Body.Close()
		}
		// 409 is the normal answer once the definition exists; any other
		// refusal shows up as the override failing below.
	}
	var failed, kept []string
	for _, key := range seeddata.ShowcaseFeatureFlags {
		if f, ok := current[key]; ok {
			if f.OverrideEnabled != nil && !*f.OverrideEnabled {
				kept = append(kept, key)
				continue
			}
			if f.OverrideEnabled != nil || f.Enabled {
				continue // already on here
			}
		}
		resp, err := client.Put("/api/v1/feature-flags/"+url.PathEscape(key)+"/override", map[string]bool{"enabled": true})
		if err != nil {
			failed = append(failed, key)
			continue
		}
		if resp.StatusCode >= 400 {
			failed = append(failed, fmt.Sprintf("%s (%s)", key, resp.Status))
		} else {
			fmt.Fprintf(out, "  + feature flag %s enabled for workspace %s\n", key, wsID)
		}
		_ = resp.Body.Close()
	}
	if len(kept) > 0 {
		fmt.Fprintf(out, "  = feature flags left off (this workspace disabled them): %s\n", strings.Join(kept, ", "))
	}
	if len(failed) > 0 {
		fmt.Fprintf(out, "  ! showcase feature flags not enabled: %s; items behind them may be skipped\n", strings.Join(failed, ", "))
	}
}

// seedShowcaseCraftAvailability asks which native engines this server runs, so
// an unavailable one is skipped before an empty Page is created for it. An
// unanswered question is an error, not a "no": the line then says the check
// failed rather than that the engine is missing.
func seedShowcaseCraftAvailability(client *cli.Client) (map[string]bool, error) {
	resp, err := client.Get("/api/v1/documents/status")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, seedPageError(resp)
	}
	var status struct {
		Word bool `json:"wordcraft_native" yaml:"wordcraft_native"`
		Grid bool `json:"gridcraft_native" yaml:"gridcraft_native"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&status); err != nil {
		return nil, fmt.Errorf("unreadable answer: %w", err)
	}
	return map[string]bool{"wordcraft": status.Word, "gridcraft": status.Grid}, nil
}

// showcasePage is the part of GET /api/v1/pages/{slug} that says whether the
// Page already holds content of any kind.
type showcasePage struct {
	NativeCraftApplication string `json:"native_craft_application" yaml:"native_craft_application"`
	HasDocument            bool   `json:"has_document" yaml:"has_document"`
	HasWorkbook            bool   `json:"has_workbook" yaml:"has_workbook"`
	HasMooring             bool   `json:"has_mooring" yaml:"has_mooring"`
	HasCollaboration       bool   `json:"has_collaboration" yaml:"has_collaboration"`
	HasProject             bool   `json:"has_project" yaml:"has_project"`
	HasApplication         bool   `json:"has_application" yaml:"has_application"`
	HasArtifactProject     bool   `json:"has_artifact_project" yaml:"has_artifact_project"`
	HasSlides              bool   `json:"has_slides" yaml:"has_slides"`
	Panels                 []any  `json:"panels" yaml:"panels"`
}

// emptyShell is an allow-empty Page with nothing attached: what a native item
// looks like after its Page was created and its initialization refused.
func (p showcasePage) emptyShell() bool {
	return p.NativeCraftApplication == "" && len(p.Panels) == 0 && !p.HasDocument && !p.HasWorkbook &&
		!p.HasMooring && !p.HasCollaboration && !p.HasProject && !p.HasApplication && !p.HasArtifactProject && !p.HasSlides
}

var errShowcaseLookup = errors.New("lookup failed")

// seedShowcaseLookup reads the Page holding slug. nil with a nil error means no
// such Page. Any other answer is reported and treated as "do not touch".
func seedShowcaseLookup(client *cli.Client, out io.Writer, slug string) (*showcasePage, error) {
	resp, err := client.Get("/api/v1/pages/" + pagePathEscape(slug))
	if err != nil {
		fmt.Fprintf(out, "  = %s: skipped (cannot check whether it exists: %v)\n", slug, err)
		return nil, errShowcaseLookup
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
		var page showcasePage
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&page); err != nil {
			fmt.Fprintf(out, "  = %s: skipped (unreadable Page answer: %v)\n", slug, err)
			return nil, errShowcaseLookup
		}
		return &page, nil
	}
	fmt.Fprintf(out, "  = %s: skipped (cannot check whether it exists: %v)\n", slug, seedPageError(resp))
	return nil, errShowcaseLookup
}

// seedShowcaseGet returns the bytes of a 200 answer, or the server's sentence.
func seedShowcaseGet(client *cli.Client, path string) ([]byte, error) {
	resp, err := client.Get(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, seedPageError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// seedShowcasePost posts body and returns the response bytes of a 2xx answer,
// or the server's own sentence.
func seedShowcasePost(client *cli.Client, path string, body any) ([]byte, int, error) {
	resp, err := client.Post(path, body)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, seedPageError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, err
}

func seedShowcaseCraft(client *cli.Client, out io.Writer, item seeddata.ShowcaseCraftDef) showcaseOutcome {
	page, err := seedShowcaseLookup(client, out, item.Slug)
	if err != nil {
		return showcaseUnavailable
	}
	success := showcaseCreated
	if page != nil {
		if !page.emptyShell() {
			return showcaseExists
		}
		// The shell an earlier run created before initialization was refused.
		// The server initializes only an empty Page its caller owns, so this
		// cannot convert anybody's content.
		success = showcaseCompleted
	} else {
		_, status, err := seedShowcasePost(client, "/api/v1/pages", map[string]any{
			"slug": item.Slug, "name": item.Name, "panels": []any{}, "allow_empty": true,
		})
		if status == http.StatusConflict {
			return showcaseExists
		}
		if err != nil {
			fmt.Fprintf(out, "  = %s: skipped (page create: %v)\n", item.Slug, err)
			return showcaseUnavailable
		}
	}
	raw, _, err := seedShowcasePost(client, "/api/v1/pages/"+pagePathEscape(item.Slug)+"/craft/initialize", struct {
		Application string `json:"application_id" yaml:"application_id"`
		Bytes       []byte `json:"bytes" yaml:"bytes"`
	}{item.Application, seeddata.ShowcaseFile(item.File)})
	if err != nil {
		fmt.Fprintf(out, "  ! %s: incomplete (%s refused initialization: %v); the empty Page is kept and the next seed initializes it\n", item.Slug, showcaseCraftLabel[item.Application], err)
		return showcaseIncomplete
	}
	seedShowcaseReleaseLease(client, out, item.Slug, raw)
	return success
}

// seedShowcaseReleaseLease closes what initialization opened. The initialize
// answer admits a native session that holds the edit lease; the CLI leaves it
// to expire, but a seed that did the same would make the first person to open
// the document wait for, or take over, a lease nobody is using. The token is
// used for this one request and never printed.
func seedShowcaseReleaseLease(client *cli.Client, out io.Writer, slug string, raw []byte) {
	var admission struct {
		Token      string `json:"session_token" yaml:"session_token"`
		Generation string `json:"generation" yaml:"generation"`
	}
	if json.Unmarshal(raw, &admission) != nil || len(admission.Token) != 64 || admission.Generation == "" {
		fmt.Fprintf(out, "  ! %s: initialized, but the session answer was unreadable; its lease expires on its own\n", slug)
		return
	}
	scoped := client.WithHeader("X-Craft-Session", admission.Token)
	// Same one-attempt transport as `crewship craft`: a redirect must not
	// replay the private session header elsewhere.
	httpClient := *scoped.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	scoped.HTTPClient = &httpClient
	if _, _, err := seedShowcasePost(scoped, "/api/v1/pages/"+pagePathEscape(slug)+"/craft/lease/release",
		map[string]string{"generation": admission.Generation}); err != nil {
		fmt.Fprintf(out, "  ! %s: initialized, but releasing the edit lease failed (%v); it expires on its own\n", slug, err)
	}
}

func seedShowcaseArtifact(client *cli.Client, out io.Writer, item seeddata.ShowcaseArtifactDef) showcaseOutcome {
	page, err := seedShowcaseLookup(client, out, item.Slug)
	if err != nil {
		return showcaseUnavailable
	}
	if page != nil {
		// Only the data half can be missing from an artifact that exists: the
		// manifest is saved atomically with the Page. A collection that is
		// still empty at code version 1 is exactly what a refused batch leaves.
		if len(item.Data) == 0 || !page.HasArtifactProject {
			return showcaseExists
		}
		wrote, failed := seedShowcaseArtifactData(client, out, item, true)
		switch {
		case failed:
			return showcaseIncomplete
		case wrote:
			return showcaseCompleted
		}
		return showcaseExists
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(seeddata.ShowcaseFile(item.File), &body); err != nil {
		fmt.Fprintf(out, "  = %s: skipped (manifest: %v)\n", item.Slug, err)
		return showcaseUnavailable
	}
	body["slug"], _ = json.Marshal(item.Slug)
	body["name"], _ = json.Marshal(item.Name)
	_, status, err := seedShowcasePost(client, "/api/v1/pages/artifacts", body)
	if status == http.StatusConflict {
		return showcaseExists
	}
	if err != nil {
		fmt.Fprintf(out, "  = %s: skipped (%s artifacts unavailable: %v)\n", item.Slug, item.Kind, err)
		return showcaseUnavailable
	}
	if _, failed := seedShowcaseArtifactData(client, out, item, false); failed {
		return showcaseIncomplete
	}
	return showcaseCreated
}

// seedShowcaseArtifactData writes each declared batch. With onlyIfEmpty it
// first reads the collection and leaves one that holds any document alone.
func seedShowcaseArtifactData(client *cli.Client, out io.Writer, item seeddata.ShowcaseArtifactDef, onlyIfEmpty bool) (wrote, failed bool) {
	for _, data := range item.Data {
		path := "/api/v1/pages/" + pagePathEscape(item.Slug) + "/artifact/data/" + pagePathEscape(data.Collection)
		batch, err := artifacts.DecodeDataBatch(seeddata.ShowcaseFile(data.File))
		if err == nil && onlyIfEmpty {
			var raw []byte
			if raw, err = seedShowcaseGet(client, fmt.Sprintf("%s?version=%d", path, batch.BaseVersion)); err == nil {
				var current struct {
					Documents []json.RawMessage `json:"documents" yaml:"documents"`
				}
				if err = json.Unmarshal(raw, &current); err == nil && len(current.Documents) > 0 {
					continue
				}
			}
		}
		if err == nil {
			var raw []byte
			if raw, err = artifacts.MarshalDataJSON(batch); err == nil {
				_, _, err = seedShowcasePost(client, path, bytes.NewReader(raw))
			}
		}
		if err != nil {
			fmt.Fprintf(out, "  ! %s: incomplete (data %s not written: %v); the next seed writes it\n", item.Slug, data.Collection, err)
			failed = true
			continue
		}
		wrote = true
	}
	return wrote, failed
}

func seedShowcaseDesign(client *cli.Client, out io.Writer, item seeddata.ShowcaseDesignDef) showcaseOutcome {
	page, err := seedShowcaseLookup(client, out, item.Slug)
	if err != nil {
		return showcaseUnavailable
	}
	base := "/api/v1/pages/" + pagePathEscape(item.Slug) + "/design"
	success := showcaseCreated
	if page != nil {
		// A canvas with no layers that the seeding account may edit is the one
		// an earlier run created before its layers were refused. Any layer at
		// all is somebody's content.
		raw, err := seedShowcaseGet(client, base)
		var view struct {
			CanEdit bool              `json:"can_edit" yaml:"can_edit"`
			Nodes   []json.RawMessage `json:"nodes" yaml:"nodes"`
		}
		if err != nil || json.Unmarshal(raw, &view) != nil || !view.CanEdit || len(view.Nodes) > 0 {
			return showcaseExists
		}
		success = showcaseCompleted
	} else {
		_, status, err := seedShowcasePost(client, "/api/v1/pages/design", map[string]any{"slug": item.Slug, "name": item.Name})
		if status == http.StatusConflict {
			return showcaseExists
		}
		if err != nil {
			fmt.Fprintf(out, "  = %s: skipped (Design unavailable: %v)\n", item.Slug, err)
			return showcaseUnavailable
		}
	}
	if _, _, err := seedShowcasePost(client, base+"/operations", bytes.NewReader(seeddata.ShowcaseFile(item.File))); err != nil {
		fmt.Fprintf(out, "  ! %s: incomplete (canvas created, layers refused: %v); the next seed applies them\n", item.Slug, err)
		return showcaseIncomplete
	}
	if _, _, err := seedShowcasePost(client, base+"/snapshots", map[string]any{"message": ""}); err != nil {
		// The live canvas is the content; a missing retained version only
		// means the history starts at the next save.
		fmt.Fprintf(out, "  ! %s: canvas filled, but its first retained version was refused (%v)\n", item.Slug, err)
	}
	return success
}
