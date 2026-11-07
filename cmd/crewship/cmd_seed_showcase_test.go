package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
)

// A 64-hex session value built at run time, so no literal resembles a credential.
var showcaseTestToken = strings.Repeat("5a", 32)

// showcaseServer is a fake of the handful of routes the showcase phase uses.
// Each knob turns one capability off the way a real server refuses it.
type showcaseServer struct {
	wordNative, gridNative bool
	initializeStatus       int            // 0 = 201
	flagStatus             int            // 0 = 200
	artifactStatus         map[string]int // slug -> refusal status
	designStatus           int            // 0 = 201
	existing               map[string]bool

	mu       *sync.Mutex
	calls    []string
	pages    map[string]map[string]any
	released map[string]string // slug -> generation
	session  map[string]bool   // slug -> X-Craft-Session matched on release
}

func (s *showcaseServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, r.Method+" "+path)
		if r.URL.Query().Get("workspace_id") != "cshowcasewsabcdefghi" {
			t.Errorf("%s %s without workspace_id", r.Method, path)
		}
		reply := func(status int, body any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		}
		refuse := func(status int) { reply(status, map[string]string{"error": "refused by test"}) }
		switch {
		case r.Method == "GET" && path == "/api/v1/documents/status":
			reply(200, map[string]bool{"enabled": true, "wordcraft_native": s.wordNative, "gridcraft_native": s.gridNative})
		case r.Method == "POST" && path == "/api/v1/feature-flags":
			reply(409, map[string]string{"error": "exists"})
		case r.Method == "PUT" && strings.HasPrefix(path, "/api/v1/feature-flags/"):
			if s.flagStatus != 0 {
				refuse(s.flagStatus)
				return
			}
			reply(200, map[string]bool{"enabled": true})
		case r.Method == "POST" && path == "/api/v1/pages":
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if body["allow_empty"] != true {
				t.Errorf("craft page %v created without allow_empty", body["slug"])
			}
			s.pages[body["slug"].(string)] = body
			reply(201, map[string]any{"slug": body["slug"]})
		case r.Method == "POST" && path == "/api/v1/pages/artifacts":
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			slug, _ := body["slug"].(string)
			if st := s.artifactStatus[slug]; st != 0 {
				refuse(st)
				return
			}
			if body["name"] == "" || body["entrypoint"] == nil || body["files"] == nil {
				t.Errorf("artifact %s: incomplete manifest %v", slug, body)
			}
			s.pages[slug] = body
			reply(201, map[string]any{"slug": slug, "version": 1})
		case r.Method == "POST" && path == "/api/v1/pages/design":
			if s.designStatus != 0 {
				refuse(s.designStatus)
				return
			}
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			s.pages[body["slug"].(string)] = body
			reply(201, body)
		case strings.HasPrefix(path, "/api/v1/pages/"):
			rest := strings.TrimPrefix(path, "/api/v1/pages/")
			slug, tail, _ := strings.Cut(rest, "/")
			switch {
			case r.Method == "GET" && tail == "":
				if s.existing[slug] {
					reply(200, map[string]string{"slug": slug})
					return
				}
				refuse(404)
			case r.Method == "POST" && tail == "craft/initialize":
				if s.initializeStatus != 0 {
					refuse(s.initializeStatus)
					return
				}
				var body struct {
					Application string `json:"application_id"`
					Bytes       []byte `json:"bytes"`
				}
				if err := json.Unmarshal(raw, &body); err != nil || len(body.Bytes) == 0 || !json.Valid(body.Bytes) {
					t.Errorf("initialize %s: bytes are not the native JSON document", slug)
				}
				reply(201, map[string]string{"session_token": showcaseTestToken, "session_id": "sess-" + slug,
					"application_id": body.Application, "page_id": "pg-" + slug, "generation": "3", "version": "1"})
			case r.Method == "POST" && tail == "craft/lease/release":
				var body map[string]string
				_ = json.Unmarshal(raw, &body)
				s.released[slug] = body["generation"]
				s.session[slug] = r.Header.Get("X-Craft-Session") == showcaseTestToken
				reply(200, map[string]any{})
			case r.Method == "POST" && strings.HasPrefix(tail, "artifact/data/"):
				if _, err := artifacts.DecodeDataBatch(raw); err != nil {
					t.Errorf("data batch %s: %v", path, err)
				}
				reply(200, map[string]any{})
			case r.Method == "POST" && tail == "design/operations":
				reply(200, map[string]any{})
			case r.Method == "POST" && tail == "design/snapshots":
				reply(201, map[string]any{"version": 1})
			default:
				t.Errorf("unexpected %s %s", r.Method, path)
				refuse(404)
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, path)
			refuse(404)
		}
	})
}

func (s *showcaseServer) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (s *showcaseServer) exact(call string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (s *showcaseServer) countSuffix(method, suffix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if strings.HasPrefix(c, method+" ") && strings.HasSuffix(c, suffix) {
			n++
		}
	}
	return n
}

func allShowcaseSlugs() map[string]bool {
	out := map[string]bool{}
	for _, c := range seeddata.ShowcaseCrafts {
		out[c.Slug] = true
	}
	for _, a := range seeddata.ShowcaseArtifacts {
		out[a.Slug] = true
	}
	for _, d := range seeddata.ShowcaseDesigns {
		out[d.Slug] = true
	}
	return out
}

func TestSeedShowcase_Phase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		server showcaseServer
		want   showcaseStats
		// expected request counts
		pageCreates, initializes, releases, artifactCreates, dataBatches, designOps, snapshots int
	}{
		{
			name:        "everything available",
			server:      showcaseServer{wordNative: true, gridNative: true},
			want:        showcaseStats{Created: 12},
			pageCreates: 7, initializes: 7, releases: 7, artifactCreates: 4, dataBatches: 2, designOps: 1, snapshots: 1,
		},
		{
			name:            "native craft unavailable skips documents only",
			server:          showcaseServer{},
			want:            showcaseStats{Created: 5, Unavailable: 7},
			artifactCreates: 4, dataBatches: 2, designOps: 1, snapshots: 1,
		},
		{
			name:        "gridcraft unavailable skips sheets only",
			server:      showcaseServer{wordNative: true},
			want:        showcaseStats{Created: 9, Unavailable: 3},
			pageCreates: 4, initializes: 4, releases: 4, artifactCreates: 4, dataBatches: 2, designOps: 1, snapshots: 1,
		},
		{
			name:        "initialize refused is per item",
			server:      showcaseServer{wordNative: true, gridNative: true, initializeStatus: 503},
			want:        showcaseStats{Created: 5, Unavailable: 7},
			pageCreates: 7, initializes: 7, artifactCreates: 4, dataBatches: 2, designOps: 1, snapshots: 1,
		},
		{
			name:            "slides flag off skips the deck only",
			server:          showcaseServer{artifactStatus: map[string]int{"prodejni-prezentace": 404}},
			want:            showcaseStats{Created: 4, Unavailable: 8},
			artifactCreates: 4, dataBatches: 2, designOps: 1, snapshots: 1,
		},
		{
			name:            "dashboard and poll refused are per item",
			server:          showcaseServer{artifactStatus: map[string]int{"obchod-dashboard": 404, "hlasovani-akce": 404}},
			want:            showcaseStats{Created: 3, Unavailable: 9},
			artifactCreates: 4, designOps: 1, snapshots: 1,
		},
		{
			name:            "design disabled skips the canvas",
			server:          showcaseServer{designStatus: 404},
			want:            showcaseStats{Created: 4, Unavailable: 8},
			artifactCreates: 4, dataBatches: 2,
		},
		{
			name:            "feature flags forbidden does not stop the phase",
			server:          showcaseServer{flagStatus: 403},
			want:            showcaseStats{Created: 5, Unavailable: 7},
			artifactCreates: 4, dataBatches: 2, designOps: 1, snapshots: 1,
		},
		{
			name:   "existing slugs are skipped",
			server: showcaseServer{wordNative: true, gridNative: true, existing: allShowcaseSlugs()},
			want:   showcaseStats{Skipped: 12},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := tc.server
			s.pages, s.released, s.session = map[string]map[string]any{}, map[string]string{}, map[string]bool{}
			s.mu = &sync.Mutex{}
			srv := httptest.NewServer(s.handler(t))
			defer srv.Close()
			var out bytes.Buffer
			stats, err := seedShowcase(context.Background(), cli.NewClient(srv.URL, "tok", "cshowcasewsabcdefghi"), &out)
			if err != nil {
				t.Fatalf("seedShowcase returned %v; the phase is fail-soft\n%s", err, out.String())
			}
			if stats != tc.want {
				t.Errorf("stats = %+v, want %+v\n%s", stats, tc.want, out.String())
			}
			checks := []struct {
				what      string
				got, want int
			}{
				{"page creates", s.exact("POST /api/v1/pages"), tc.pageCreates},
				{"initializes", s.countSuffix("POST", "/craft/initialize"), tc.initializes},
				{"lease releases", s.countSuffix("POST", "/craft/lease/release"), tc.releases},
				{"artifact creates", s.count("POST /api/v1/pages/artifacts"), tc.artifactCreates},
				{"data batches", s.count("POST /api/v1/pages/obchod-dashboard/artifact/data/") + s.count("POST /api/v1/pages/hlasovani-akce/artifact/data/"), tc.dataBatches},
				{"design operations", s.countSuffix("POST", "/design/operations"), tc.designOps},
				{"design snapshots", s.countSuffix("POST", "/design/snapshots"), tc.snapshots},
				{"flag overrides", s.count("PUT /api/v1/feature-flags/"), len(seeddata.ShowcaseFeatureFlags)},
			}
			for _, c := range checks {
				if c.got != c.want {
					t.Errorf("%s = %d, want %d\ncalls: %v", c.what, c.got, c.want, s.calls)
				}
			}
			for slug, gen := range s.released {
				if gen != "3" || !s.session[slug] {
					t.Errorf("%s: release sent generation %q, session header ok=%v; want the admission's generation and token", slug, gen, s.session[slug])
				}
			}
			if strings.Contains(out.String(), showcaseTestToken) {
				t.Error("the native session token was printed")
			}
		})
	}
}

// The flags have to be created before the first override: craft_version_history
// has no instance-wide definition on a fresh install.
func TestSeedShowcase_CreatesFlagDefinitionBeforeOverride(t *testing.T) {
	t.Parallel()
	s := showcaseServer{}
	s.pages, s.released, s.session = map[string]map[string]any{}, map[string]string{}, map[string]bool{}
	s.mu = &sync.Mutex{}
	srv := httptest.NewServer(s.handler(t))
	defer srv.Close()
	if _, err := seedShowcase(context.Background(), cli.NewClient(srv.URL, "tok", "cshowcasewsabcdefghi"), io.Discard); err != nil {
		t.Fatal(err)
	}
	created, overridden := -1, -1
	for i, c := range s.calls {
		if c == "POST /api/v1/feature-flags" && created < 0 {
			created = i
		}
		if c == "PUT /api/v1/feature-flags/craft_version_history/override" {
			overridden = i
		}
	}
	if created < 0 || overridden < 0 || created > overridden {
		t.Errorf("create at %d, override at %d; want the definition created first\n%v", created, overridden, s.calls)
	}
}

func TestSeedShowcase_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := seedShowcase(ctx, cli.NewClient("http://127.0.0.1:1", "tok", "cshowcasewsabcdefghi"), io.Discard); err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// The catalogue is the contract with seed-showcase.sh: the same slugs, names
// and files, every file embedded and shaped as its endpoint takes it.
func TestShowcaseCatalogue_FilesExist(t *testing.T) {
	t.Parallel()
	wantSlugs := []string{
		"obchodni-nabidka", "zapis-z-porady", "shrnuti-smlouvy", "playbook-onboarding",
		"cenova-nabidka", "rozpocet-2027", "pipeline-q4",
		"obchod-dashboard", "hlasovani-akce", "kalkulacka-roi", "prodejni-prezentace",
		"landing-page",
	}
	var got []string
	seen := map[string]bool{}
	for _, c := range seeddata.ShowcaseCrafts {
		got = append(got, c.Slug)
		if c.Application != "wordcraft" && c.Application != "gridcraft" {
			t.Errorf("%s: application %q", c.Slug, c.Application)
		}
		if raw := seeddata.ShowcaseFile(c.File); !json.Valid(raw) || len(raw) > craftNativeBytesLimit {
			t.Errorf("%s: %s is not a native JSON document within the limit", c.Slug, c.File)
		}
	}
	for _, a := range seeddata.ShowcaseArtifacts {
		got = append(got, a.Slug)
		var manifest map[string]json.RawMessage
		if err := json.Unmarshal(seeddata.ShowcaseFile(a.File), &manifest); err != nil {
			t.Errorf("%s: %v", a.Slug, err)
		}
		for k := range manifest {
			if k != "entrypoint" && k != "files" && k != "message" {
				t.Errorf("%s: manifest field %q is refused by the server", a.Slug, k)
			}
		}
		for _, d := range a.Data {
			if _, err := artifacts.DecodeDataBatch(seeddata.ShowcaseFile(d.File)); err != nil {
				t.Errorf("%s/%s: %v", a.Slug, d.Collection, err)
			}
		}
	}
	for _, d := range seeddata.ShowcaseDesigns {
		got = append(got, d.Slug)
		var ops struct {
			Operations []json.RawMessage `json:"operations"`
		}
		if err := json.Unmarshal(seeddata.ShowcaseFile(d.File), &ops); err != nil || len(ops.Operations) == 0 {
			t.Errorf("%s: design operations unreadable: %v", d.Slug, err)
		}
	}
	if strings.Join(got, ",") != strings.Join(wantSlugs, ",") {
		t.Errorf("slugs = %v, want %v", got, wantSlugs)
	}
	for _, s := range got {
		if seen[s] {
			t.Errorf("duplicate slug %s", s)
		}
		seen[s] = true
	}
	for _, p := range seeddata.Pages {
		if seen[p.Slug] {
			t.Errorf("showcase slug %s collides with a demo Page", p.Slug)
		}
	}
}

func TestSeedShowcase_SkipFlagRegistered(t *testing.T) {
	t.Parallel()
	f := seedCmd.Flags().Lookup("skip-showcase")
	if f == nil || f.DefValue != "false" {
		t.Fatalf("--skip-showcase must exist and default to false (showcase on), got %+v", f)
	}
	found := false
	for _, name := range uiTourExclusiveFlags {
		found = found || name == "skip-showcase"
	}
	if !found {
		t.Error("--skip-showcase is a default-phase flag and must be refused together with --ui-tour")
	}
}
