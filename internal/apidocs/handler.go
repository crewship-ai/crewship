package apidocs

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

//go:embed assets
var assets embed.FS

// BasePath is where this surface is mounted. Every link the pages emit is
// relative to the same origin — nothing here is fetched from anywhere else,
// which is what keeps the docs working on an air-gapped install.
const BasePath = "/openapi"

var tmpl = template.Must(template.New("pages").Funcs(template.FuncMap{
	"lower": strings.ToLower,
}).ParseFS(assets, "assets/*.html"))

// NewHandler returns the HTML rendering of the given OpenAPI document, mounted
// at BasePath. It never returns an error: a spec it cannot parse is a build
// bug, and it is reported as a 500 with the reason rather than a 200 that
// renders an empty shell — a docs page that answers 200 with nothing in it is
// the same lie GET /openapi.json told when the SPA catch-all owned that path.
//
// The document is parsed and indexed once, here, rather than lazily on first
// view: measured on the current 3 MB / 536-operation spec that is ~20 ms of
// server startup and ~5 MB retained, which is not worth a sync.Once and the
// concurrency surface that comes with it. Rendering a page afterwards touches
// only the index. Revisit if either number grows an order of magnitude.
func NewHandler(spec []byte) http.Handler {
	h := &handler{}
	ix, err := newIndex(spec)
	if err != nil {
		h.parseErr = err
		return h
	}
	h.ix = ix
	return h
}

type handler struct {
	ix       *index
	parseErr error
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.parseErr != nil {
		http.Error(w, "openapi docs unavailable: the embedded spec did not parse: "+h.parseErr.Error(), http.StatusInternalServerError)
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, BasePath)
	switch {
	case rest == "" || rest == "/":
		h.renderIndex(w)
	case rest == "/ui.css":
		h.asset(w, "assets/ui.css", "text/css; charset=utf-8")
	case rest == "/ui.js":
		h.asset(w, "assets/ui.js", "text/javascript; charset=utf-8")
	case rest == "/schemas":
		h.renderSchemas(w)
	case strings.HasPrefix(rest, "/op/"):
		h.renderOperation(w, strings.TrimPrefix(rest, "/op/"))
	case strings.HasPrefix(rest, "/schema/"):
		h.renderSchema(w, strings.TrimPrefix(rest, "/schema/"))
	default:
		h.notFound(w, "No such page.")
	}
}

func (h *handler) asset(w http.ResponseWriter, name, contentType string) {
	b, err := assets.ReadFile(name)
	if err != nil {
		h.notFound(w, "No such asset.")
		return
	}
	w.Header().Set("Content-Type", contentType)
	// Assets change only when the binary does, but a stale one against a new
	// binary is a broken page rather than a wrong fact, so a short cache is
	// the right trade.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// chrome is what every page shows in its header.
type chrome struct {
	Title          string
	SpecTitle      string
	SpecVersion    string
	OpenAPIVersion string
	Base           string
}

func (h *handler) chrome(title string) chrome {
	return chrome{
		Title:          title,
		SpecTitle:      h.ix.doc.Info.Title,
		SpecVersion:    h.ix.doc.Info.Version,
		OpenAPIVersion: h.ix.doc.OpenAPI,
		Base:           BasePath,
	}
}

func (h *handler) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "openapi docs render failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (h *handler) notFound(w http.ResponseWriter, msg string) {
	if h.ix == nil {
		http.Error(w, msg, http.StatusNotFound)
		return
	}
	h.render(w, http.StatusNotFound, "notfound", struct {
		chrome
		Message string
	}{h.chrome("Not found"), msg})
}

type indexPage struct {
	chrome
	Description        string
	PathCount          int
	OpCount            int
	TagCount           int
	SchemaCount        int
	PublicCount        int
	UnreachableSchemas int
	Groups             []tagGroup
}

func (h *handler) renderIndex(w http.ResponseWriter) {
	h.render(w, http.StatusOK, "index", indexPage{
		chrome:             h.chrome("API operations"),
		Description:        h.ix.doc.Info.Description,
		PathCount:          len(h.ix.doc.Paths),
		OpCount:            len(h.ix.ops),
		TagCount:           len(h.ix.tags),
		SchemaCount:        len(h.ix.schemas),
		PublicCount:        h.ix.PublicOps,
		UnreachableSchemas: h.ix.UnreachableSchemas,
		Groups:             h.ix.tags,
	})
}

type secOption struct {
	Schemes []secScheme
}

type secScheme struct {
	Name string
	How  string
}

type paramGroup struct {
	In     string
	Params []paramView
}

type paramView struct {
	Name        string
	Type        string
	Required    bool
	Deprecated  bool
	Description string
	Enum        []string
}

type contentView struct {
	MediaType string
	Tree      node
}

type responseView struct {
	Status      string
	Class       string // s2, s4, s5 … drives the colour of the status pill
	Description string
	Contents    []contentView
	Empty       bool
}

// statusClass buckets a response code by its first digit so success, client
// error and server error read differently at a glance.
func statusClass(status string) string {
	if status == "" {
		return "sx"
	}
	switch status[0] {
	case '1', '2', '3', '4', '5':
		return "s" + status[:1]
	default:
		return "sx"
	}
}

type opPage struct {
	chrome
	Op           *opEntry
	Security     []secOption
	Params       []paramGroup
	HasBody      bool
	BodyRequired bool
	BodyDesc     string
	Body         []contentView
	Responses    []responseView
}

func (h *handler) renderOperation(w http.ResponseWriter, id string) {
	e, ok := h.ix.opByID[id]
	if !ok || id == "" {
		// The requested id is deliberately not echoed back. It would be
		// escaped by the template, but a page that reflects any path segment
		// is a page a scanner has to reason about; there is nothing to gain
		// here by repeating what the reader just typed.
		h.notFound(w, "No operation with that id. It may have been renamed, or removed from this build.")
		return
	}
	op := e.Op

	p := opPage{chrome: h.chrome(e.Method + " " + e.Path), Op: e}

	for _, alt := range op.Security {
		var o secOption
		names := make([]string, 0, len(alt))
		for n := range alt {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			o.Schemes = append(o.Schemes, secScheme{Name: n, How: h.describeScheme(n)})
		}
		p.Security = append(p.Security, o)
	}

	byIn := map[string][]paramView{}
	for _, pa := range op.Parameters {
		if pa == nil {
			continue
		}
		v := paramView{
			Name:        pa.Name,
			Required:    pa.Required,
			Deprecated:  pa.Deprecated,
			Description: pa.Description,
			Type:        "any",
		}
		if pa.Schema != nil {
			v.Type = typeLabel(pa.Schema)
			for _, en := range pa.Schema.Enum {
				v.Enum = append(v.Enum, toString(en))
			}
		}
		byIn[pa.In] = append(byIn[pa.In], v)
	}
	for _, in := range []string{"path", "query", "header", "cookie"} {
		if list, ok := byIn[in]; ok {
			sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
			p.Params = append(p.Params, paramGroup{In: in, Params: list})
		}
	}

	if op.RequestBody != nil {
		p.HasBody = true
		p.BodyRequired = op.RequestBody.Required
		p.BodyDesc = op.RequestBody.Description
		p.Body = h.contents(op.RequestBody.Content)
	}

	statuses := make([]string, 0, len(op.Responses))
	for s := range op.Responses {
		statuses = append(statuses, s)
	}
	sort.Slice(statuses, func(i, j int) bool { return statusLess(statuses[i], statuses[j]) })
	for _, s := range statuses {
		r := op.Responses[s]
		rv := responseView{Status: s, Class: statusClass(s)}
		if r != nil {
			rv.Description = r.Description
			rv.Contents = h.contents(r.Content)
		}
		rv.Empty = len(rv.Contents) == 0
		p.Responses = append(p.Responses, rv)
	}

	h.render(w, http.StatusOK, "operation", p)
}

func (h *handler) contents(c map[string]mediaType) []contentView {
	types := make([]string, 0, len(c))
	for t := range c {
		types = append(types, t)
	}
	sort.Strings(types)
	out := make([]contentView, 0, len(types))
	for _, t := range types {
		out = append(out, contentView{MediaType: t, Tree: h.ix.tree(c[t].Schema, "", false)})
	}
	return out
}

func (h *handler) describeScheme(name string) string {
	s, ok := h.ix.doc.Components.SecuritySchemes[name]
	if !ok || s == nil {
		return "scheme not described in components.securitySchemes"
	}
	switch s.Type {
	case "http":
		if strings.EqualFold(s.Scheme, "bearer") {
			return "Authorization: Bearer <token>"
		}
		return "HTTP " + s.Scheme + " authentication"
	case "apiKey":
		switch s.In {
		case "cookie":
			return "Cookie: " + s.Name
		case "header":
			return "Header: " + s.Name
		case "query":
			return "Query parameter: " + s.Name
		}
		return "API key " + s.Name
	}
	return s.Type
}

type schemasPage struct {
	chrome
	Schemas            []*schemaEntry
	UnreachableSchemas int
}

func (h *handler) renderSchemas(w http.ResponseWriter) {
	h.render(w, http.StatusOK, "schemas", schemasPage{
		chrome:             h.chrome("Schemas"),
		Schemas:            h.ix.schemas,
		UnreachableSchemas: h.ix.UnreachableSchemas,
	})
}

type schemaPage struct {
	chrome
	Entry *schemaEntry
	Tree  node
}

func (h *handler) renderSchema(w http.ResponseWriter, name string) {
	e, ok := h.ix.schemaBy[name]
	if !ok || name == "" {
		h.notFound(w, "No schema with that name in components.schemas.")
		return
	}
	h.render(w, http.StatusOK, "schema", schemaPage{
		chrome: h.chrome("Schema " + name),
		Entry:  e,
		// Named root: on this page the tree IS the schema, so the root row
		// carries its name. The "(body)" fallback in the template is for the
		// request/response roots on an operation page, which have none.
		Tree: h.ix.tree(e.Schema, e.Name, false),
	})
}

// statusLess orders response codes numerically, keeping any non-numeric key
// ("default") last.
func statusLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return ai < bi
	case aerr == nil:
		return true
	case berr == nil:
		return false
	default:
		return a < b
	}
}

// toString renders an enum member the way a caller would have to send it: a
// string as itself, anything else in its JSON form.
func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return "null"
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
