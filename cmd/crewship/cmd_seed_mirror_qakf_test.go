package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

// showcaseMirrorDocs is the number of mirrored Docs items seedShowcase runs;
// the shared showcase fake has Docs off, so each counts as unavailable there.
const showcaseMirrorDocs = 1

type mirrorQakfNode struct {
	Type    string            `json:"type"`
	Attrs   map[string]any    `json:"attrs"`
	Content []json.RawMessage `json:"content"`
	Text    *string           `json:"text"`
	Marks   []struct {
		Type  string         `json:"type"`
		Attrs map[string]any `json:"attrs"`
	} `json:"marks"`
}

func (n mirrorQakfNode) children(t *testing.T) []mirrorQakfNode {
	t.Helper()
	out := make([]mirrorQakfNode, len(n.Content))
	for i, raw := range n.Content {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&out[i]); err != nil {
			t.Fatalf("node under %s: %v", n.Type, err)
		}
	}
	return out
}

func (n mirrorQakfNode) text(t *testing.T) string {
	if n.Text != nil {
		return *n.Text
	}
	var b strings.Builder
	for _, c := range n.children(t) {
		b.WriteString(c.text(t))
	}
	return b.String()
}

// The rules tools/docs-service/schema-core.mjs applies to a schema 3 body
// (validateJSON), restated so a bad file fails here rather than on a seed.
var (
	mirrorQakfBlockID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
	mirrorQakfHref    = regexp.MustCompile(`^https?://[^\s@/]+/\S*$`)
	mirrorQakfContent = map[string][]string{
		"doc": {"heading", "paragraph", "bulletList", "orderedList", "table", "codeBlock"}, "heading": {"text"}, "paragraph": {"text"}, "codeBlock": {"text"},
		"bulletList": {"listItem"}, "orderedList": {"listItem"}, "listItem": {"paragraph", "bulletList", "orderedList"},
		"table": {"tableRow"}, "tableRow": {"tableHeader", "tableCell"}, "tableHeader": {"paragraph"}, "tableCell": {"paragraph"},
	}
	mirrorQakfAttrs = map[string][]string{"heading": {"id", "level"}, "orderedList": {"id", "start"}, "tableCell": {"id", "colspan", "rowspan", "colwidth"}, "tableHeader": {"id", "colspan", "rowspan", "colwidth"}}
)

func mirrorQakfDocument(t *testing.T) mirrorQakfNode {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(seeddata.ShowcaseFile(seeddata.MirrorQakfDoc.File)))
	dec.DisallowUnknownFields()
	var doc mirrorQakfNode
	if err := dec.Decode(&doc); err != nil || doc.Type != "doc" || doc.Attrs != nil {
		t.Fatalf("document.json is not a closed doc root: %v", err)
	}
	return doc
}

func TestMirrorQakf_DocumentIsASchema3Body(t *testing.T) {
	t.Parallel()
	raw := seeddata.ShowcaseFile(seeddata.MirrorQakfDoc.File)
	// The initialize route takes 2 MiB and the engine 500 000 characters of JSON.
	if compact := new(bytes.Buffer); json.Compact(compact, raw) != nil || compact.Len() > 500000 {
		t.Fatalf("document.json is %d bytes compacted; the engine refuses more than 500000", compact.Len())
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	var visit func(n mirrorQakfNode, depth int)
	visit = func(n mirrorQakfNode, depth int) {
		counts[n.Type]++
		if depth > 40 {
			t.Fatal("deeper than the engine's 40 levels")
		}
		if n.Type == "text" {
			if n.Text == nil || *n.Text == "" || n.Attrs != nil || n.Content != nil {
				t.Errorf("invalid text node %+v", n)
			}
			if len(n.Marks) > 1 {
				t.Errorf("text %q carries %d marks; schema 3 keeps at most one", *n.Text, len(n.Marks))
			}
			for _, m := range n.Marks {
				switch m.Type {
				case "code":
					if len(m.Attrs) != 0 {
						t.Errorf("code mark with attrs %v", m.Attrs)
					}
				case "link":
					href, _ := m.Attrs["href"].(string)
					if u, err := url.Parse(href); len(m.Attrs) != 1 || err != nil || u.String() != href || !mirrorQakfHref.MatchString(href) {
						t.Errorf("link %q is not a canonical HTTP(S) address the schema keeps", href)
					}
				default:
					t.Errorf("mark %q is not in schema 3", m.Type)
				}
			}
			return
		}
		if len(n.Marks) > 0 {
			t.Errorf("%s carries block marks", n.Type)
		}
		if n.Type != "doc" {
			id, _ := n.Attrs["id"].(string)
			if !mirrorQakfBlockID.MatchString(id) || seen[id] {
				t.Errorf("%s: invalid or duplicate block id %q", n.Type, id)
			}
			seen[id] = true
			allowed := mirrorQakfAttrs[n.Type]
			if allowed == nil {
				allowed = []string{"id"}
			}
			for key, value := range n.Attrs {
				ok := false
				for _, a := range allowed {
					ok = ok || a == key
				}
				if !ok {
					t.Errorf("%s %s: attribute %q is refused", n.Type, id, key)
				}
				if (key == "colspan" || key == "rowspan") && value != float64(1) || key == "colwidth" && value != nil {
					t.Errorf("%s %s: only simple tables are accepted (%s=%v)", n.Type, id, key, value)
				}
			}
			if lvl, ok := n.Attrs["level"]; n.Type == "heading" && (!ok || lvl.(float64) < 1 || lvl.(float64) > 3) {
				t.Errorf("heading %s: level %v", id, lvl)
			}
		}
		kids := n.children(t)
		if (n.Type == "listItem" || n.Type == "tableHeader" || n.Type == "tableCell") && (len(kids) == 0 || kids[0].Type != "paragraph") {
			t.Errorf("%s must start with a paragraph", n.Type)
		}
		for _, c := range kids {
			ok := false
			for _, allowed := range mirrorQakfContent[n.Type] {
				ok = ok || allowed == c.Type
			}
			if !ok {
				t.Errorf("%s may not hold %s", n.Type, c.Type)
			}
			visit(c, depth+1)
		}
	}
	visit(mirrorQakfDocument(t), 0)
	// The original's structure, by kind: 12 headings on the overview tab and the
	// notes tab's title; 2 tables plus one each for the diagram and the chart;
	// 10 code blocks; the nested numbered list; 3 bullet lists and 2 check lists.
	for kind, want := range map[string]int{"heading": 13, "table": 4, "codeBlock": 10, "orderedList": 2, "bulletList": 5} {
		if counts[kind] != want {
			t.Errorf("%d %s blocks, want %d", counts[kind], kind, want)
		}
	}
}

// Every text block of the original (both tabs that hold text) is in the copy
// word for word; the saved original itself is checked against the outline's
// block hashes by convert.py.
func TestMirrorQakf_TextIsVerbatim(t *testing.T) {
	t.Parallel()
	var copied []string
	var collect func(n mirrorQakfNode)
	collect = func(n mirrorQakfNode) {
		switch n.Type {
		case "heading", "paragraph", "codeBlock":
			copied = append(copied, strings.TrimPrefix(strings.TrimPrefix(n.text(t), "☑ "), "☐ "))
			return
		}
		for _, c := range n.children(t) {
			collect(c)
		}
	}
	collect(mirrorQakfDocument(t))
	have := map[string]bool{}
	for _, s := range copied {
		have[s] = true
	}
	total := 0
	for _, tab := range []string{"tab-prehled.json", "tab-poznamky.json", "tab-1.json"} {
		var saved struct {
			XML string `json:"xml"`
		}
		if err := json.Unmarshal(seeddata.ShowcaseFile("mirror/qakf/original/"+tab), &saved); err != nil {
			t.Fatal(err)
		}
		for _, text := range mirrorQakfOriginalBlocks(t, saved.XML) {
			total++
			if !have[text] {
				t.Errorf("%s: block %q is missing from the copy", tab, text)
			}
		}
	}
	if total != 95 {
		t.Errorf("original holds %d text blocks, want 95", total)
	}
}

// mirrorQakfOriginalBlocks returns the text of every non-empty paragraph or
// code block in a Claude Docs outline; chips contribute their visible words.
func mirrorQakfOriginalBlocks(t *testing.T, doc string) []string {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	var out []string
	var cur *strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if el.Name.Local == "paragraph" || el.Name.Local == "codeBlock" {
				cur = &strings.Builder{}
			}
			for _, a := range el.Attr {
				if cur != nil && (el.Name.Local == "date" && a.Name.Local == "value" || el.Name.Local == "mention" && (a.Name.Local == "name" || a.Name.Local == "label")) {
					cur.WriteString(a.Value)
				}
			}
		case xml.CharData:
			if cur != nil {
				cur.Write(el)
			}
		case xml.EndElement:
			if (el.Name.Local == "paragraph" || el.Name.Local == "codeBlock") && cur != nil {
				if cur.Len() > 0 {
					out = append(out, cur.String())
				}
				cur = nil
			}
		}
	}
}

// mirrorQakfServer fakes the routes the step uses, keeping state across runs.
type mirrorQakfServer struct {
	mu             sync.Mutex
	docs           bool
	statusCode     int
	createStatus   int
	initStatus     int
	page           *mirrorQakfPage
	creates, inits int
	t              *testing.T
}

type mirrorQakfPage struct {
	id                     string
	document, otherContent bool
}

func (s *mirrorQakfServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	reply := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	slug := seeddata.MirrorQakfDoc.Slug
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/documents/status":
		if s.statusCode != 0 {
			reply(s.statusCode, map[string]string{"error": "refused by test"})
			return
		}
		reply(200, map[string]bool{"enabled": s.docs})
	case r.Method == "GET" && r.URL.Path == "/api/v1/pages/"+slug:
		if s.page == nil {
			reply(404, map[string]string{"error": "not found"})
			return
		}
		panels := []any{}
		if s.page.otherContent {
			panels = append(panels, map[string]any{"id": "p1"})
		}
		reply(200, map[string]any{"id": s.page.id, "slug": slug, "has_document": s.page.document, "panels": panels})
	case r.Method == "POST" && r.URL.Path == "/api/v1/pages":
		s.creates++
		if s.createStatus != 0 {
			reply(s.createStatus, map[string]string{"error": "refused by test"})
			return
		}
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["slug"] != slug || body["name"] != seeddata.MirrorQakfDoc.Name || body["allow_empty"] != true {
			s.t.Errorf("page create %v; want the mirror's slug and name, allow_empty", body)
		}
		s.page = &mirrorQakfPage{id: "pg-mirror-qakf"}
		reply(201, map[string]any{"id": s.page.id, "slug": slug})
	case r.Method == "POST" && r.URL.Path == "/api/v1/pages/"+slug+"/document/initialize":
		s.inits++
		if s.initStatus != 0 {
			reply(s.initStatus, map[string]string{"error": "refused by test"})
			return
		}
		if s.page == nil || s.page.document || s.page.otherContent {
			s.t.Errorf("initialize on a Page that is not an empty shell: %+v", s.page)
		}
		var body map[string]json.RawMessage
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		_ = dec.Decode(&body)
		var req mirrorQakfInitialize
		if err := json.Unmarshal(raw, &req); err != nil || len(body) != 4 {
			s.t.Errorf("initialize body has keys %v; want id, schema, page_id, body", body)
		}
		var sent, want any
		_ = json.Unmarshal(req.Body, &sent)
		_ = json.Unmarshal(seeddata.ShowcaseFile(seeddata.MirrorQakfDoc.File), &want)
		if req.ID != seeddata.MirrorQakfDoc.OperationID || req.Schema != 3 || req.PageID != s.page.id {
			s.t.Errorf("initialize id=%q schema=%d page_id=%q; want the stable id, schema 3 and the Page's id", req.ID, req.Schema, req.PageID)
		}
		if !jsonEqual(sent, want) {
			s.t.Error("initialize body is not document.json")
		}
		s.page.document = true
		reply(200, map[string]any{"rev": 1, "schema": 3})
	default:
		s.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		reply(404, map[string]string{"error": "unexpected"})
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestSeedShowcaseMirrorQakf(t *testing.T) {
	t.Parallel()
	type run struct {
		docs                                 bool
		statusCode, createStatus, initStatus int
		want                                 showcaseOutcome
		creates, inits                       int
		line                                 string
	}
	ok := func(want showcaseOutcome, creates, inits int, line string) run {
		return run{docs: true, want: want, creates: creates, inits: inits, line: line}
	}
	cases := []struct {
		name  string
		setup *mirrorQakfPage
		runs  []run
	}{
		{name: "fresh server: created, then left alone", runs: []run{ok(showcaseCreated, 1, 1, ""), ok(showcaseExists, 0, 0, "")}},
		{name: "Docs disabled: nothing created", runs: []run{{want: showcaseUnavailable, line: "skipped (Docs are not enabled on this server)"}}},
		{name: "status check failure", runs: []run{{docs: true, statusCode: 500, want: showcaseUnavailable, line: "skipped (Docs status check failed"}}},
		{name: "page create refused", runs: []run{{docs: true, createStatus: 403, want: showcaseUnavailable, creates: 1, line: "skipped (page create"}}},
		{name: "slug taken between lookup and create", runs: []run{{docs: true, createStatus: 409, want: showcaseExists, creates: 1}}},
		{
			name: "refused initialization is completed by the next run, without a second Page",
			runs: []run{
				{docs: true, initStatus: 503, want: showcaseIncomplete, creates: 1, inits: 1, line: "incomplete (Docs refused initialization"},
				ok(showcaseCompleted, 0, 1, ""),
				ok(showcaseExists, 0, 0, ""),
			},
		},
		{name: "a Page that already holds the document is untouched", setup: &mirrorQakfPage{id: "pg-x", document: true}, runs: []run{ok(showcaseExists, 0, 0, "")}},
		{name: "a Page holding anything else is untouched", setup: &mirrorQakfPage{id: "pg-x", otherContent: true}, runs: []run{ok(showcaseExists, 0, 0, "")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &mirrorQakfServer{t: t, page: tc.setup}
			srv := httptest.NewServer(s)
			defer srv.Close()
			for i, r := range tc.runs {
				s.mu.Lock()
				s.docs, s.statusCode, s.createStatus, s.initStatus, s.creates, s.inits = r.docs, r.statusCode, r.createStatus, r.initStatus, 0, 0
				s.mu.Unlock()
				var out bytes.Buffer
				got := seedShowcaseMirrorQakf(cli.NewClient(srv.URL, "tok", showcaseTestWS), &out)
				if got != r.want || s.creates != r.creates || s.inits != r.inits {
					t.Errorf("run %d: outcome %d creates %d inits %d; want %d, %d, %d\n%s", i, got, s.creates, s.inits, r.want, r.creates, r.inits, out.String())
				}
				if r.line != "" && !strings.Contains(out.String(), r.line) {
					t.Errorf("run %d: output lacks %q\n%s", i, r.line, out.String())
				}
			}
		})
	}
}

// The step is wired into the showcase phase and counted in its summary.
func TestSeedShowcase_RunsTheQakfMirror(t *testing.T) {
	t.Parallel()
	s := &mirrorQakfServer{t: t, docs: true}
	shared := newShowcaseServer(showcaseKnobs{wordNative: true, gridNative: true, docs: true})
	shared.fill()
	mux := http.NewServeMux()
	mirror := "/api/v1/pages/" + seeddata.MirrorQakfDoc.Slug
	mux.Handle(mirror, s)
	mux.Handle(mirror+"/", s)
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/pages" && bytes.Contains(peekBody(r), []byte(seeddata.MirrorQakfDoc.Slug)) {
			s.ServeHTTP(w, r)
			return
		}
		shared.handler(t).ServeHTTP(w, r)
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	var out bytes.Buffer
	stats, err := seedShowcase(t.Context(), cli.NewClient(srv.URL, "tok", showcaseTestWS), &out)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Created != 1 || stats.Skipped != 12 || s.inits != 1 {
		t.Errorf("stats %+v, initializes %d; want the mirror created next to 12 present items\n%s", stats, s.inits, out.String())
	}
	if !strings.Contains(out.String(), "+ "+seeddata.MirrorQakfDoc.Slug) {
		t.Errorf("no created line for the mirror\n%s", out.String())
	}
}

// peekBody reads the request body and puts it back for the next handler.
func peekBody(r *http.Request) []byte {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return raw
}
