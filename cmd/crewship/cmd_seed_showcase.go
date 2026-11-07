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
// Idempotent by slug: an item whose Page already exists is left untouched, so
// a re-seed never overwrites a document somebody has since edited.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

// showcaseStats counts items by outcome. Every catalogue item lands in exactly
// one bucket.
type showcaseStats struct {
	Created, Skipped, Unavailable int
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

	available := seedShowcaseCraftAvailability(client)
	for _, item := range seeddata.ShowcaseCrafts {
		if err := ctx.Err(); err != nil {
			return stats, err
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

	fmt.Fprintf(out, "  %d showcase item(s) created, %d already present, %d unavailable on this server\n",
		stats.Created, stats.Skipped, stats.Unavailable)
	return stats, nil
}

type showcaseOutcome int

const (
	showcaseCreated showcaseOutcome = iota
	showcaseExists
	showcaseUnavailable
)

func seedShowcaseCount(stats *showcaseStats, out io.Writer, slug string, o showcaseOutcome) {
	switch o {
	case showcaseCreated:
		fmt.Fprintf(out, "  + %s\n", slug)
		stats.Created++
	case showcaseExists:
		fmt.Fprintf(out, "  = %s: already exists, left unchanged\n", slug)
		stats.Skipped++
	default:
		stats.Unavailable++
	}
}

// seedShowcaseFlags turns on the workspace flags the showcase surfaces need.
// A flag that cannot be set (the seeding account is not an instance admin, or
// the server has no such flag) is reported once; the items behind it then
// report their own refusal.
func seedShowcaseFlags(client *cli.Client, out io.Writer) {
	for _, key := range seeddata.ShowcaseFeatureFlagsCreateFirst {
		resp, err := client.Post("/api/v1/feature-flags", map[string]any{"key": key, "enabled": false, "percentage": 0})
		if err == nil {
			_ = resp.Body.Close()
		}
		// 409 is the normal answer once the definition exists; any other
		// refusal shows up as the override failing below.
	}
	var failed []string
	for _, key := range seeddata.ShowcaseFeatureFlags {
		resp, err := client.Put("/api/v1/feature-flags/"+key+"/override", map[string]bool{"enabled": true})
		if err != nil {
			failed = append(failed, key)
			continue
		}
		if resp.StatusCode >= 400 {
			failed = append(failed, fmt.Sprintf("%s (%s)", key, resp.Status))
		}
		_ = resp.Body.Close()
	}
	if len(failed) > 0 {
		fmt.Fprintf(out, "  ! showcase feature flags not enabled: %v; items behind them may be skipped\n", failed)
	}
}

// seedShowcaseCraftAvailability asks which native engines this server runs, so
// an unavailable one is skipped before an empty Page is created for it.
func seedShowcaseCraftAvailability(client *cli.Client) map[string]bool {
	resp, err := client.Get("/api/v1/documents/status")
	if err != nil {
		return map[string]bool{}
	}
	defer func() { _ = resp.Body.Close() }()
	var status struct {
		Word bool `json:"wordcraft_native" yaml:"wordcraft_native"`
		Grid bool `json:"gridcraft_native" yaml:"gridcraft_native"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&status) != nil {
		return map[string]bool{}
	}
	return map[string]bool{"wordcraft": status.Word, "gridcraft": status.Grid}
}

// seedShowcaseExists reports whether a Page already holds the slug. An answer
// that is neither found nor not-found is reported and treated as "do not touch".
func seedShowcaseExists(client *cli.Client, out io.Writer, slug string) (exists, ok bool) {
	resp, err := client.Get("/api/v1/pages/" + pagePathEscape(slug))
	if err != nil {
		fmt.Fprintf(out, "  = %s: skipped (cannot check whether it exists: %v)\n", slug, err)
		return false, false
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusOK:
		return true, true
	case resp.StatusCode == http.StatusNotFound:
		return false, true
	}
	fmt.Fprintf(out, "  = %s: skipped (cannot check whether it exists: %v)\n", slug, seedPageError(resp))
	return false, false
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
	if exists, ok := seedShowcaseExists(client, out, item.Slug); !ok {
		return showcaseUnavailable
	} else if exists {
		return showcaseExists
	}
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
	raw, _, err := seedShowcasePost(client, "/api/v1/pages/"+pagePathEscape(item.Slug)+"/craft/initialize", struct {
		Application string `json:"application_id" yaml:"application_id"`
		Bytes       []byte `json:"bytes" yaml:"bytes"`
	}{item.Application, seeddata.ShowcaseFile(item.File)})
	if err != nil {
		fmt.Fprintf(out, "  = %s: skipped (%s refused initialization: %v; the empty Page is kept)\n", item.Slug, showcaseCraftLabel[item.Application], err)
		return showcaseUnavailable
	}
	seedShowcaseReleaseLease(client, out, item.Slug, raw)
	return showcaseCreated
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
	if exists, ok := seedShowcaseExists(client, out, item.Slug); !ok {
		return showcaseUnavailable
	} else if exists {
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
	for _, data := range item.Data {
		batch, err := artifacts.DecodeDataBatch(seeddata.ShowcaseFile(data.File))
		if err == nil {
			var raw []byte
			if raw, err = artifacts.MarshalDataJSON(batch); err == nil {
				_, _, err = seedShowcasePost(client, "/api/v1/pages/"+pagePathEscape(item.Slug)+"/artifact/data/"+pagePathEscape(data.Collection), bytes.NewReader(raw))
			}
		}
		if err != nil {
			fmt.Fprintf(out, "  ! %s: data %s not written (%v); the artifact opens without it\n", item.Slug, data.Collection, err)
		}
	}
	return showcaseCreated
}

func seedShowcaseDesign(client *cli.Client, out io.Writer, item seeddata.ShowcaseDesignDef) showcaseOutcome {
	if exists, ok := seedShowcaseExists(client, out, item.Slug); !ok {
		return showcaseUnavailable
	} else if exists {
		return showcaseExists
	}
	_, status, err := seedShowcasePost(client, "/api/v1/pages/design", map[string]any{"slug": item.Slug, "name": item.Name})
	if status == http.StatusConflict {
		return showcaseExists
	}
	if err != nil {
		fmt.Fprintf(out, "  = %s: skipped (Design unavailable: %v)\n", item.Slug, err)
		return showcaseUnavailable
	}
	base := "/api/v1/pages/" + pagePathEscape(item.Slug) + "/design"
	if _, _, err := seedShowcasePost(client, base+"/operations", bytes.NewReader(seeddata.ShowcaseFile(item.File))); err != nil {
		fmt.Fprintf(out, "  ! %s: canvas created, but its layers were refused (%v)\n", item.Slug, err)
		return showcaseCreated
	}
	if _, _, err := seedShowcasePost(client, base+"/snapshots", map[string]any{"message": ""}); err != nil {
		fmt.Fprintf(out, "  ! %s: canvas filled, snapshot skipped (%v)\n", item.Slug, err)
	}
	return showcaseCreated
}
