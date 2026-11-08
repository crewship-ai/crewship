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

const showcaseTestWS = "cshowcasewsabcdefghi"

// showcaseKnobs turn one capability off the way a real server refuses it.
type showcaseKnobs struct {
	wordNative, gridNative bool
	statusCode             int            // documents/status; 0 = 200
	initializeStatus       int            // 0 = 201
	flagStatus             int            // override PUT; 0 = 200
	artifactStatus         map[string]int // slug -> create refusal
	dataStatus             int            // data batch POST; 0 = 200
	designStatus           int            // design create; 0 = 201
	designOpsStatus        int            // design operations; 0 = 200
}

type fakeShowcasePage struct {
	native   string
	artifact bool
	design   bool
	panels   int
	data     map[string]int
	nodes    int
}

type fakeShowcaseFlag struct {
	enabled  bool
	override *bool
}

// showcaseServer is a stateful fake of the routes the showcase phase uses, so
// a second run sees what the first one left behind.
type showcaseServer struct {
	mu       *sync.Mutex
	knobs    showcaseKnobs
	pages    map[string]*fakeShowcasePage
	flags    map[string]*fakeShowcaseFlag
	calls    []string
	released map[string]string // slug -> generation
	session  map[string]bool   // slug -> X-Craft-Session matched on release
}

func newShowcaseServer(k showcaseKnobs) *showcaseServer {
	s := &showcaseServer{mu: &sync.Mutex{}, knobs: k, pages: map[string]*fakeShowcasePage{},
		flags: map[string]*fakeShowcaseFlag{}, released: map[string]string{}, session: map[string]bool{}}
	for _, key := range seeddata.ShowcaseFeatureFlags {
		if key != "craft_version_history" { // undefined on a fresh install
			s.flags[key] = &fakeShowcaseFlag{}
		}
	}
	return s
}

func (s *showcaseServer) setKnobs(k showcaseKnobs) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.knobs, s.calls = k, nil
}

// fill makes every catalogue slug hold complete content.
func (s *showcaseServer) fill() {
	for _, c := range seeddata.ShowcaseCrafts {
		s.pages[c.Slug] = &fakeShowcasePage{native: c.Application}
	}
	for _, a := range seeddata.ShowcaseArtifacts {
		p := &fakeShowcasePage{artifact: true, data: map[string]int{}}
		for _, d := range a.Data {
			p.data[d.Collection] = 3
		}
		s.pages[a.Slug] = p
	}
	for _, d := range seeddata.ShowcaseDesigns {
		s.pages[d.Slug] = &fakeShowcasePage{design: true, nodes: 4}
	}
}

func (s *showcaseServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		s.mu.Lock()
		defer s.mu.Unlock()
		k := s.knobs
		s.calls = append(s.calls, r.Method+" "+path)
		if r.URL.Query().Get("workspace_id") != showcaseTestWS {
			t.Errorf("%s %s outside the seeded workspace (workspace_id=%q)", r.Method, path, r.URL.Query().Get("workspace_id"))
		}
		reply := func(status int, body any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
		}
		refuse := func(status int) { reply(status, map[string]string{"error": "refused by test"}) }
		switch {
		case r.Method == "GET" && path == "/api/v1/documents/status":
			if k.statusCode != 0 {
				refuse(k.statusCode)
				return
			}
			reply(200, map[string]bool{"enabled": true, "wordcraft_native": k.wordNative, "gridcraft_native": k.gridNative})
		case r.Method == "GET" && path == "/api/v1/feature-flags":
			var list []map[string]any
			for key, f := range s.flags {
				item := map[string]any{"key": key, "enabled": f.enabled}
				if f.override != nil {
					item["override_enabled"] = *f.override
				}
				list = append(list, item)
			}
			reply(200, list)
		case r.Method == "POST" && path == "/api/v1/feature-flags":
			var body struct {
				Key     string `json:"key"`
				Enabled bool   `json:"enabled"`
			}
			_ = json.Unmarshal(raw, &body)
			if body.Enabled {
				t.Errorf("flag %s defined ON instance-wide; the seed may only define it OFF", body.Key)
			}
			if _, ok := s.flags[body.Key]; ok {
				refuse(409)
				return
			}
			s.flags[body.Key] = &fakeShowcaseFlag{}
			reply(201, map[string]any{"key": body.Key})
		case r.Method == "PUT" && strings.HasPrefix(path, "/api/v1/feature-flags/") && strings.HasSuffix(path, "/override"):
			key := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/feature-flags/"), "/override")
			f, ok := s.flags[key]
			if k.flagStatus != 0 || !ok {
				refuse(map[bool]int{true: k.flagStatus, false: 404}[k.flagStatus != 0])
				return
			}
			on := true
			f.override = &on
			reply(200, map[string]bool{"enabled": true})
		case r.Method == "POST" && path == "/api/v1/pages":
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if body["allow_empty"] != true {
				t.Errorf("craft page %v created without allow_empty", body["slug"])
			}
			s.pages[body["slug"].(string)] = &fakeShowcasePage{}
			reply(201, map[string]any{"slug": body["slug"]})
		case r.Method == "POST" && path == "/api/v1/pages/artifacts":
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			slug, _ := body["slug"].(string)
			if st := k.artifactStatus[slug]; st != 0 {
				refuse(st)
				return
			}
			if body["name"] == "" || body["entrypoint"] == nil || body["files"] == nil {
				t.Errorf("artifact %s: incomplete manifest %v", slug, body)
			}
			s.pages[slug] = &fakeShowcasePage{artifact: true, data: map[string]int{}}
			reply(201, map[string]any{"slug": slug, "version": 1})
		case r.Method == "POST" && path == "/api/v1/pages/design":
			if k.designStatus != 0 {
				refuse(k.designStatus)
				return
			}
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			s.pages[body["slug"].(string)] = &fakeShowcasePage{design: true}
			reply(201, body)
		case strings.HasPrefix(path, "/api/v1/pages/"):
			slug, tail, _ := strings.Cut(strings.TrimPrefix(path, "/api/v1/pages/"), "/")
			p := s.pages[slug]
			if p == nil {
				refuse(404)
				return
			}
			switch {
			case r.Method == "GET" && tail == "":
				panels := make([]any, p.panels)
				reply(200, map[string]any{"slug": slug, "native_craft_application": p.native, "has_artifact_project": p.artifact, "panels": panels})
			case r.Method == "POST" && tail == "craft/initialize":
				if k.initializeStatus != 0 {
					refuse(k.initializeStatus)
					return
				}
				if p.native != "" || p.artifact || p.design || p.panels > 0 {
					t.Errorf("initialize %s on a Page that already holds content", slug)
				}
				var body struct {
					Application string `json:"application_id"`
					Bytes       []byte `json:"bytes"`
				}
				if err := json.Unmarshal(raw, &body); err != nil || len(body.Bytes) == 0 || !json.Valid(body.Bytes) {
					t.Errorf("initialize %s: bytes are not the native JSON document", slug)
				}
				p.native = body.Application
				reply(201, map[string]string{"session_token": showcaseTestToken, "session_id": "sess-" + slug,
					"application_id": body.Application, "page_id": "pg-" + slug, "generation": "3", "version": "1"})
			case r.Method == "POST" && tail == "craft/lease/release":
				var body map[string]string
				_ = json.Unmarshal(raw, &body)
				s.released[slug] = body["generation"]
				s.session[slug] = r.Header.Get("X-Craft-Session") == showcaseTestToken
				reply(200, map[string]any{})
			case strings.HasPrefix(tail, "artifact/data/"):
				coll := strings.TrimPrefix(tail, "artifact/data/")
				if r.Method == "GET" {
					if r.URL.Query().Get("version") != "1" {
						t.Errorf("data read %s without the batch's code version", path)
					}
					reply(200, map[string]any{"documents": make([]map[string]any, p.data[coll])})
					return
				}
				if k.dataStatus != 0 {
					refuse(k.dataStatus)
					return
				}
				batch, err := artifacts.DecodeDataBatch(raw)
				if err != nil {
					t.Errorf("data batch %s: %v", path, err)
				}
				if p.data[coll] > 0 {
					t.Errorf("data batch %s over existing documents", path)
				}
				p.data[coll] = len(batch.Documents)
				reply(200, map[string]any{})
			case r.Method == "GET" && tail == "design":
				reply(200, map[string]any{"can_edit": true, "nodes": make([]map[string]any, p.nodes)})
			case r.Method == "POST" && tail == "design/operations":
				if k.designOpsStatus != 0 {
					refuse(k.designOpsStatus)
					return
				}
				if p.nodes > 0 {
					t.Errorf("design operations applied over %d existing layers", p.nodes)
				}
				var ops struct {
					Operations []json.RawMessage `json:"operations"`
				}
				_ = json.Unmarshal(raw, &ops)
				p.nodes = len(ops.Operations)
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

func (s *showcaseServer) count(match func(string) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if match(c) {
			n++
		}
	}
	return n
}

func exactly(call string) func(string) bool { return func(c string) bool { return c == call } }
func postSuffix(suffix string) func(string) bool {
	return func(c string) bool { return strings.HasPrefix(c, "POST ") && strings.HasSuffix(c, suffix) }
}
func prefixed(prefix string) func(string) bool {
	return func(c string) bool { return strings.HasPrefix(c, prefix) }
}

// showcaseWant is one run's expected outcome and request counts.
type showcaseWant struct {
	stats                                                                                 showcaseStats
	pageCreates, initializes, releases, artifactCreates, dataWrites, designOps, overrides int
	lines                                                                                 []string
}

func runShowcaseOnce(t *testing.T, s *showcaseServer, url string, want showcaseWant) {
	t.Helper()
	var out bytes.Buffer
	stats, err := seedShowcase(context.Background(), cli.NewClient(url, "tok", showcaseTestWS), &out)
	if err != nil {
		t.Fatalf("seedShowcase returned %v; the phase is fail-soft\n%s", err, out.String())
	}
	// Mirrored reference artifacts (slug prefix "ref-") are fixed-content
	// extras with their own tests; here they only shift the totals by what
	// each one reported.
	for _, a := range seeddata.ShowcaseArtifacts {
		if !strings.HasPrefix(a.Slug, "ref-") {
			continue
		}
		switch text := out.String(); {
		case strings.Contains(text, "  + "+a.Slug+"\n"):
			want.stats.Created++
			want.artifactCreates++
			want.dataWrites += len(a.Data)
		case strings.Contains(text, "  + "+a.Slug+": completed"):
			want.stats.Completed++
			want.dataWrites += len(a.Data)
		case strings.Contains(text, "  ! "+a.Slug+": incomplete"):
			want.stats.Incomplete++
			want.artifactCreates++
			want.dataWrites += len(a.Data)
		case strings.Contains(text, "  = "+a.Slug+": already exists"):
			want.stats.Skipped++
		default:
			t.Errorf("mirror item %s reported no outcome\n%s", a.Slug, text)
		}
	}
	if stats != want.stats {
		t.Errorf("stats = %+v, want %+v\n%s", stats, want.stats, out.String())
	}
	checks := []struct {
		what      string
		got, want int
	}{
		{"page creates", s.count(exactly("POST /api/v1/pages")), want.pageCreates},
		{"initializes", s.count(postSuffix("/craft/initialize")), want.initializes},
		{"lease releases", s.count(postSuffix("/craft/lease/release")), want.releases},
		{"artifact creates", s.count(exactly("POST /api/v1/pages/artifacts")), want.artifactCreates},
		{"data writes", s.count(func(c string) bool { return strings.HasPrefix(c, "POST ") && strings.Contains(c, "/artifact/data/") }), want.dataWrites},
		{"design operations", s.count(postSuffix("/design/operations")), want.designOps},
		{"flag overrides", s.count(prefixed("PUT /api/v1/feature-flags/")), want.overrides},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d\ncalls: %v", c.what, c.got, c.want, s.calls)
		}
	}
	for _, line := range want.lines {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output lacks %q\n%s", line, out.String())
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
}

var allOn = showcaseKnobs{wordNative: true, gridNative: true}

func TestSeedShowcase_Phase(t *testing.T) {
	t.Parallel()
	full := showcaseWant{stats: showcaseStats{Created: 12}, pageCreates: 7, initializes: 7, releases: 7,
		artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7,
		lines: []string{"+ feature flag craft_version_history enabled for workspace " + showcaseTestWS, "+ obchodni-nabidka"}}
	cases := []struct {
		name  string
		setup func(*showcaseServer)
		runs  []showcaseKnobs
		want  []showcaseWant
	}{
		{name: "everything available", runs: []showcaseKnobs{allOn}, want: []showcaseWant{full}},
		{
			name: "native craft unavailable skips documents only", runs: []showcaseKnobs{{}},
			want: []showcaseWant{{stats: showcaseStats{Created: 5, Unavailable: 7}, artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7,
				lines: []string{"= obchodni-nabidka: skipped (native Word (WordCraft) is not available on this server)"}}},
		},
		{
			name: "status check failure is not reported as unavailable", runs: []showcaseKnobs{{statusCode: 500}},
			want: []showcaseWant{{stats: showcaseStats{Created: 5, Unavailable: 7}, artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7,
				lines: []string{"= pipeline-q4: skipped (native document status check failed: 500"}}},
		},
		{
			name: "gridcraft unavailable skips sheets only", runs: []showcaseKnobs{{wordNative: true}},
			want: []showcaseWant{{stats: showcaseStats{Created: 9, Unavailable: 3}, pageCreates: 4, initializes: 4, releases: 4,
				artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7}},
		},
		{
			name: "slides flag off skips the deck only", runs: []showcaseKnobs{{artifactStatus: map[string]int{"prodejni-prezentace": 404}}},
			want: []showcaseWant{{stats: showcaseStats{Created: 4, Unavailable: 8}, artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7}},
		},
		{
			name: "design disabled skips the canvas", runs: []showcaseKnobs{{designStatus: 404}},
			want: []showcaseWant{{stats: showcaseStats{Created: 4, Unavailable: 8}, artifactCreates: 4, dataWrites: 2, overrides: 7}},
		},
		{
			name: "design layers refused is incomplete, not created", runs: []showcaseKnobs{{designOpsStatus: 422}},
			want: []showcaseWant{{stats: showcaseStats{Created: 4, Incomplete: 1, Unavailable: 7}, artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7,
				lines: []string{"! landing-page: incomplete (canvas created, layers refused"}}},
		},
		{
			name: "feature flags forbidden does not stop the phase", runs: []showcaseKnobs{{flagStatus: 403}},
			want: []showcaseWant{{stats: showcaseStats{Created: 5, Unavailable: 7}, artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7,
				lines: []string{"! showcase feature flags not enabled: "}}},
		},
		{
			name: "a workspace that switched a flag off keeps it off",
			setup: func(s *showcaseServer) {
				off := false
				s.flags["artifact_slides"].override = &off
				s.flags["gridcraft_editor"].enabled = true
			},
			runs: []showcaseKnobs{allOn},
			want: []showcaseWant{{stats: showcaseStats{Created: 12}, pageCreates: 7, initializes: 7, releases: 7,
				artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 5,
				lines: []string{"= feature flags left off (this workspace disabled them): artifact_slides"}}},
		},
		{
			name: "existing content is never touched", setup: func(s *showcaseServer) { s.fill() },
			runs: []showcaseKnobs{allOn},
			want: []showcaseWant{{stats: showcaseStats{Skipped: 12}, overrides: 7}},
		},
		{
			// P2-1: a refused step is retried by the next run, which finishes
			// the item instead of skipping its slug forever.
			name: "refused steps are completed by the next run",
			runs: []showcaseKnobs{
				{wordNative: true, gridNative: true, initializeStatus: 503, dataStatus: 503, designOpsStatus: 503},
				allOn,
				allOn,
			},
			want: []showcaseWant{
				{stats: showcaseStats{Created: 2, Incomplete: 10}, pageCreates: 7, initializes: 7, artifactCreates: 4, dataWrites: 2, designOps: 1, overrides: 7,
					lines: []string{"! obchodni-nabidka: incomplete (native Word (WordCraft) refused initialization", "! obchod-dashboard: incomplete (data pipeline not written"}},
				{stats: showcaseStats{Completed: 10, Skipped: 2}, initializes: 7, releases: 7, dataWrites: 2, designOps: 1,
					lines: []string{"+ obchodni-nabidka: completed (an earlier seed left it incomplete)"}},
				{stats: showcaseStats{Skipped: 12}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newShowcaseServer(tc.runs[0])
			if tc.setup != nil {
				tc.setup(s)
			}
			srv := httptest.NewServer(s.handler(t))
			defer srv.Close()
			for i, knobs := range tc.runs {
				s.setKnobs(knobs)
				runShowcaseOnce(t, s, srv.URL, tc.want[i])
			}
		})
	}
}

// The flags have to be created before the first override: craft_version_history
// has no instance-wide definition on a fresh install.
func TestSeedShowcase_CreatesFlagDefinitionBeforeOverride(t *testing.T) {
	t.Parallel()
	s := newShowcaseServer(showcaseKnobs{})
	srv := httptest.NewServer(s.handler(t))
	defer srv.Close()
	if _, err := seedShowcase(context.Background(), cli.NewClient(srv.URL, "tok", showcaseTestWS), io.Discard); err != nil {
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
	if _, err := seedShowcase(ctx, cli.NewClient("http://127.0.0.1:1", "tok", showcaseTestWS), io.Discard); err != context.Canceled {
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
	// Mirrored reference items (seeddata/mirror_*.go, slug prefix "ref-")
	// append themselves and carry their own tests; the rest is fixed here.
	var base []string
	for _, s := range got {
		if !strings.HasPrefix(s, "ref-") {
			base = append(base, s)
		}
	}
	if strings.Join(base, ",") != strings.Join(wantSlugs, ",") {
		t.Errorf("slugs = %v, want %v", base, wantSlugs)
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

// P3-4: the flag gates the phase inside runSeed, not just in flag parsing.
func TestRunSeed_SkipShowcaseGatesThePhase(t *testing.T) {
	for _, tc := range []struct {
		name     string
		skip     string
		wantHits bool
	}{
		{"default runs the showcase", "false", true},
		{"--skip-showcase leaves it out", "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := covSeedStub(t)
			covSetupRunSeed(t, s)
			covSetFlag(t, seedCmd, "skip-issues", "true")
			covSetFlag(t, seedCmd, "skip-showcase", tc.skip)
			out, err := covCaptureStdout(t, func() error { return seedCmd.RunE(seedCmd, nil) })
			if err != nil {
				t.Fatalf("runSeed: %v\n%s", err, out)
			}
			hits := 0
			for _, c := range s.Calls() {
				if c.Path == "/api/v1/documents/status" || strings.HasPrefix(c.Path, "/api/v1/feature-flags") ||
					c.Path == "/api/v1/pages/artifacts" || c.Path == "/api/v1/pages/design" {
					hits++
				}
			}
			if (hits > 0) != tc.wantHits {
				t.Errorf("showcase requests = %d, want any=%v\n%s", hits, tc.wantHits, out)
			}
			if got := strings.Contains(out, "Creating showcase documents and artifacts..."); got != tc.wantHits {
				t.Errorf("showcase banner printed = %v, want %v", got, tc.wantHits)
			}
		})
	}
}
