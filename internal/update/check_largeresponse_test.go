package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// oldResponseCap is the 1 MiB ceiling the release-API readers used to impose
// on the response body. It is not referenced by check.go any more — it lives
// here so the regression tests can assert their fixtures are comfortably
// bigger than the size that used to truncate a real production response
// mid-array (B-01).
const oldResponseCap = 1 << 20

// buildReleaseListJSON renders a syntactically valid GitHub `/releases` array
// whose encoded form exceeds minBytes.
//
// The size is not artificial. Every crewship release carries ~44 assets
// (archives for six platforms, plus .pem, .sig and an SBOM in two formats),
// and GitHub inlines the full asset objects and the full release notes in
// every list entry — so `releases?per_page=30` really does return ~7.9 MB for
// this repo. The fixture mirrors that shape (padded notes + a bundle of asset
// objects per release) rather than one giant string, so it fails the same way
// production did: the JSON is well-formed, only the *reader* was too small.
func buildReleaseListJSON(t *testing.T, tags []string, minBytes int) []byte {
	t.Helper()

	type asset struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
		Size        int    `json:"size"`
	}
	type release struct {
		TagName string  `json:"tag_name"`
		HTMLURL string  `json:"html_url"`
		Body    string  `json:"body"`
		Draft   bool    `json:"draft"`
		Assets  []asset `json:"assets"`
	}

	// Pad the release notes, which is where the bulk of a real response's
	// bytes sit once the asset lists are accounted for. Split the target
	// evenly across the entries so no single value is implausibly large.
	const padUnit = "Changelog line: assorted fixes, see the release page for the full notes.\n"
	padPerRelease := minBytes/len(tags) + 4096
	pad := strings.Repeat(padUnit, padPerRelease/len(padUnit)+1)

	releases := make([]release, 0, len(tags))
	for _, tag := range tags {
		assets := make([]asset, 0, 44)
		for i := 0; i < 44; i++ {
			assets = append(assets, asset{
				Name:        fmt.Sprintf("crewship_%s_asset_%02d.tar.gz", tag, i),
				DownloadURL: fmt.Sprintf("https://github.com/crewship-ai/crewship/releases/download/%s/asset_%02d.tar.gz", tag, i),
				Size:        1234567,
			})
		}
		releases = append(releases, release{
			TagName: tag,
			HTMLURL: "https://github.com/crewship-ai/crewship/releases/tag/" + tag,
			Body:    "notes for " + tag + "\n" + pad,
			Assets:  assets,
		})
	}

	data, err := json.Marshal(releases)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if len(data) < minBytes {
		t.Fatalf("fixture is %d bytes, want at least %d — the test would not exercise the size path", len(data), minBytes)
	}
	// Sanity: the fixture must be valid JSON in full, so a parse failure can
	// only mean the reader truncated it.
	if !json.Valid(data) {
		t.Fatalf("fixture is not valid JSON")
	}
	return data
}

// TestFetchRelease_ResponseLargerThanOldCap is the regression test for B-01.
//
// Both release-API readers used to do io.ReadAll(io.LimitReader(body, 1<<20)).
// io.LimitReader reports clean EOF at the cap instead of an error, so an
// oversized response was silently cut off mid-array and json.Unmarshal failed
// with "unexpected end of JSON input" on every single call. The stable
// channel hits /releases/latest (one small object) and never noticed; the
// nightly channel hits releases?per_page=30, which for this repo is ~7.9 MB.
func TestFetchRelease_ResponseLargerThanOldCap(t *testing.T) {
	cases := []struct {
		name    string
		tags    []string
		fetch   func(context.Context, string) (string, string, string, error)
		wantTag string
	}{
		{
			// The nightly channel: the endpoint and the payload size that
			// actually broke in production.
			name:    "nightly list endpoint",
			tags:    []string{"nightly-20260722-r010", "nightly-20260721-r638", "nightly-20260720-r601"},
			fetch:   fetchLatestNightly,
			wantTag: "nightly-20260722-r010",
		},
		{
			// The same reader backs the pre-release channel, which also uses
			// a list endpoint and is one busy release cycle away from the
			// same failure.
			name:    "prerelease list endpoint",
			tags:    []string{"v0.1.0-beta.3", "v0.1.0-beta.2", "v0.1.0-beta.1"},
			fetch:   fetchLatest,
			wantTag: "v0.1.0-beta.3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := buildReleaseListJSON(t, tc.tags, 2*oldResponseCap)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer srv.Close()

			tag, notes, htmlURL, err := tc.fetch(context.Background(), srv.URL)
			if err != nil {
				t.Fatalf("fetch over a %d-byte response: %v", len(body), err)
			}
			if tag != tc.wantTag {
				t.Errorf("tag = %q, want %q", tag, tc.wantTag)
			}
			if !strings.Contains(notes, "notes for "+tc.wantTag) {
				t.Errorf("notes = %q, want the release notes of %s", notes, tc.wantTag)
			}
			if !strings.HasSuffix(htmlURL, tc.wantTag) {
				t.Errorf("html_url = %q, want the release page of %s", htmlURL, tc.wantTag)
			}
		})
	}
}

// setMaxReleaseResponseBytes shrinks the response cap for one test and
// returns a restore func, mirroring setLatestNightlyListURL. Injecting via the
// package var (rather than threading a parameter through both fetchers, which
// would only ever be passed the one value in production) keeps the production
// call sites unchanged and lets the overrun test use a few hundred KB instead
// of generating 32 MB.
func setMaxReleaseResponseBytes(n int64) func() {
	prev := maxReleaseResponseBytes
	maxReleaseResponseBytes = n
	return func() { maxReleaseResponseBytes = prev }
}

// TestFetchRelease_OverCapFailsLoudly pins the *mechanism* rather than the
// number. A bound enforced by io.LimitReader alone fails exactly the way the
// original 1 MiB cap did — clean EOF, truncated document, "unexpected end of
// JSON input" — so raising the number would leave the same undiagnosable bug
// one release-asset explosion away. An over-cap response must therefore
// report errResponseTooLarge and must NOT surface as a parse failure.
//
// The cases sit one byte apart around the cap, which also pins the boundary:
// a response exactly at the limit is legal.
func TestFetchRelease_OverCapFailsLoudly(t *testing.T) {
	endpoints := []struct {
		name  string
		tags  []string
		fetch func(context.Context, string) (string, string, string, error)
	}{
		{"nightly list endpoint", []string{"nightly-20260722-r010", "nightly-20260721-r638"}, fetchLatestNightly},
		{"prerelease list endpoint", []string{"v0.1.0-beta.3", "v0.1.0-beta.2"}, fetchLatest},
	}

	for _, ep := range endpoints {
		body := buildReleaseListJSON(t, ep.tags, 256<<10)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		defer srv.Close()

		cases := []struct {
			name       string
			cap        int64
			wantTooBig bool
		}{
			{"exactly at the cap", int64(len(body)), false},
			{"one byte over the cap", int64(len(body)) - 1, true},
		}
		for _, tc := range cases {
			t.Run(ep.name+"/"+tc.name, func(t *testing.T) {
				defer setMaxReleaseResponseBytes(tc.cap)()

				_, _, _, err := ep.fetch(context.Background(), srv.URL)
				if !tc.wantTooBig {
					if err != nil {
						t.Fatalf("a %d-byte body under a %d-byte cap should decode: %v", len(body), tc.cap, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("a %d-byte body under a %d-byte cap should be refused, got no error", len(body), tc.cap)
				}
				if !errors.Is(err, errResponseTooLarge) {
					t.Errorf("err = %v, want it to wrap errResponseTooLarge", err)
				}
				// The regression guard: the old mechanism produced a JSON
				// parse error that told the operator nothing about the size.
				if strings.Contains(err.Error(), "parse ") || strings.Contains(err.Error(), "unexpected end of JSON input") {
					t.Errorf("over-cap response surfaced as a JSON parse error: %v", err)
				}
				// The message has to name the limit to be actionable.
				if !strings.Contains(err.Error(), fmt.Sprintf("%d", tc.cap)) {
					t.Errorf("err = %v, want it to name the %d-byte limit", err, tc.cap)
				}
			})
		}
	}
}

// TestCheck_NightlyChannelOverLargeReleaseList drives the same payload through
// the public entry points, which is where the operator saw B-01: every
// `crewship self-update` and every dashboard poll on a nightly build failed
// with "parse release JSON: unexpected end of JSON input", deterministically.
func TestCheck_NightlyChannelOverLargeReleaseList(t *testing.T) {
	withTempHome(t)
	body := buildReleaseListJSON(t, []string{"nightly-20260722-r010", "nightly-20260721-r638"}, 2*oldResponseCap)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	defer setLatestNightlyListURL(srv.URL)()

	for _, tc := range []struct {
		name  string
		check func(context.Context, string) (*Result, error)
	}{
		{"Check", Check},
		{"CheckExplicit", CheckExplicit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Check caches on success; clear HOME's cache per subtest so both
			// subtests exercise the network path rather than the second one
			// reading what the first wrote.
			withTempHome(t)

			r, err := tc.check(context.Background(), "nightly-20260721-r638")
			if err != nil {
				t.Fatalf("%s over a %d-byte release list: %v", tc.name, len(body), err)
			}
			if r == nil {
				t.Fatal("expected a result for a nightly current version")
			}
			if r.Latest != "nightly-20260722-r010" {
				t.Errorf("Latest = %q, want nightly-20260722-r010", r.Latest)
			}
			if !r.Newer {
				t.Errorf("expected Newer=true, got %+v", r)
			}
		})
	}
}
