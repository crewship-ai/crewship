package memport

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/memory"
)

// ReadSource lowers a source tree into a Plan. It reads; it never
// writes. See the package doc for why applying a Plan is somebody
// else's job.
func ReadSource(fsys fs.FS, f Format, opts Options) (Plan, error) {
	names, err := walkFiles(fsys)
	if err != nil {
		return Plan{}, err
	}
	b := newBuilder()
	switch f {
	case FormatCrewship:
		err = readCrewship(fsys, names, b, opts)
	case FormatOKF:
		err = readOKF(fsys, names, b)
	case FormatNanoClaw:
		err = readNanoClaw(fsys, names, b, opts)
	case FormatOpenClaw:
		err = readOpenClaw(fsys, names, b, opts)
	default:
		return Plan{}, fmt.Errorf("memport: unsupported format %q", f)
	}
	if err != nil {
		return Plan{}, err
	}
	return b.plan(f), nil
}

var dateFileRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// maxSourceFileBytes bounds one source document. The largest ceiling any
// memory file has is 30 KB (daily logs); this is generous next to that
// and still refuses to load somebody's DVD image because it was named
// AGENT.md. Oversized files are reported, never silently dropped.
const maxSourceFileBytes = 1 << 20 // 1 MiB

// readCapped reads one source file, refusing anything over the per-file
// ceiling. Returns ok=false having already recorded the skip.
func readCapped(fsys fs.FS, b *builder, name string) ([]byte, bool) {
	if info, err := fs.Stat(fsys, name); err == nil && info.Size() > maxSourceFileBytes {
		b.skip(name, fmt.Sprintf("larger than the %d-byte limit for one memory document", maxSourceFileBytes))
		return nil, false
	}
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		b.skip(name, "unreadable: "+err.Error())
		return nil, false
	}
	if len(body) > maxSourceFileBytes {
		b.skip(name, fmt.Sprintf("larger than the %d-byte limit for one memory document", maxSourceFileBytes))
		return nil, false
	}
	return body, true
}

// validRelPath enforces the invariant Doc.RelPath documents: a relative,
// forward-slashed path under a .memory directory. The OKF reader takes
// this value from a bundle somebody else wrote, so it is exactly the
// field that must not be trusted on the strength of its documentation.
func validRelPath(rel string) bool {
	if rel == "" || path.IsAbs(rel) || strings.Contains(rel, "\\") {
		return false
	}
	if rel != path.Clean(rel) {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// --- OpenClaw ---------------------------------------------------------

func readOpenClaw(fsys fs.FS, names []string, b *builder, opts Options) error {
	for _, n := range names {
		dir, base := path.Dir(n), path.Base(n)

		// Classify BEFORE reading. A transcript under sessions/ may be a
		// dead symlink, a socket, or simply unreadable; reading it first
		// let one file the importer was never going to use abort the
		// whole import — and slurped a multi-gigabyte vector index into
		// memory just to discard it.
		if top := topDir(n); top == "vectors" || top == "sessions" {
			b.skip(n, "derived data — embeddings and transcripts are rebuilt locally, never imported")
			continue
		}
		if !strings.EqualFold(path.Ext(base), ".md") {
			b.skip(n, "not a markdown document")
			continue
		}
		if dir != "." && dir != "memory" {
			b.skip(n, "outside the OpenClaw memory layout")
			continue
		}

		body, ok := readCapped(fsys, b, n)
		if !ok {
			continue
		}

		if dir == "." {
			switch base {
			case "SOUL.md", "IDENTITY.md":
				b.merge("PERSONA.md", memory.TierAgent, ScopeAgent, n, body)
			case "MEMORY.md":
				b.merge("AGENT.md", memory.TierAgent, ScopeAgent, n, body)
			case "AGENTS.md":
				b.merge("CREW.md", memory.TierCrew, ScopeCrew, n, body)
			case "USER.md":
				if opts.OperatorSlug == "" {
					b.skip(n, "operator-facing card needs a target person; re-run with the operator slug")
					continue
				}
				b.merge("peers/"+opts.OperatorSlug+".md", memory.TierAgent, ScopeAgent, n, body)
			default:
				b.merge("AGENT.md", memory.TierAgent, ScopeAgent, n, body)
			}
			continue
		}

		if dir == "memory" {
			stem := strings.TrimSuffix(base, path.Ext(base))
			if dateFileRe.MatchString(stem) {
				b.merge("daily/"+stem+".md", memory.TierAgent, ScopeAgent, n, body)
				continue
			}
			// Topic notes are long-term knowledge with no tier of
			// their own; they become sections of AGENT.md.
			b.merge("AGENT.md", memory.TierAgent, ScopeAgent, n, body)
			continue
		}
	}
	return nil
}

// --- NanoClaw ---------------------------------------------------------

func readNanoClaw(fsys fs.FS, names []string, b *builder, opts Options) error {
	group, err := pickNanoClawGroup(names, opts.Group)
	if err != nil {
		return err
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "groups/") {
			b.skip(n, "outside groups/")
			continue
		}
		rel := strings.TrimPrefix(n, "groups/")
		slash := strings.Index(rel, "/")
		if slash < 0 {
			b.skip(n, "stray file at the groups/ root")
			continue
		}
		g, within := rel[:slash], rel[slash+1:]

		if path.Dir(within) != "." {
			b.skip(n, "task logs and nested state are not memory")
			continue
		}
		if !strings.EqualFold(path.Ext(within), ".md") {
			b.skip(n, "not a markdown document")
			continue
		}
		body, ok := readCapped(fsys, b, n)
		if !ok {
			continue
		}

		switch g {
		case "global":
			// Global memory is what every group shares — that is our
			// crew-shared tier, not an agent's private one.
			b.merge("CREW.md", memory.TierCrew, ScopeCrew, n, body)
		case group:
			b.merge("AGENT.md", memory.TierAgent, ScopeAgent, n, body)
		default:
			b.skip(n, "belongs to group "+g+", which this import did not select")
		}
	}
	return nil
}

// pickNanoClawGroup resolves which group's memory becomes the agent's.
// With several candidates and no choice supplied it refuses and names
// them: merging two groups produces one agent that believes it was in
// both conversations, which no later edit can untangle.
func pickNanoClawGroup(names []string, want string) (string, error) {
	seen := map[string]bool{}
	var candidates []string
	for _, n := range names {
		if !strings.HasPrefix(n, "groups/") {
			continue
		}
		rel := strings.TrimPrefix(n, "groups/")
		slash := strings.Index(rel, "/")
		if slash < 0 {
			continue
		}
		g := rel[:slash]
		if g == "global" || seen[g] {
			continue
		}
		seen[g] = true
		candidates = append(candidates, g)
	}
	sort.Strings(candidates)

	if want != "" {
		if !seen[want] {
			return "", fmt.Errorf("memport: group %q not found in the source (available: %s)",
				want, strings.Join(candidates, ", "))
		}
		return want, nil
	}
	switch len(candidates) {
	case 0:
		return "", nil // global-only source: crew memory with no agent tier
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("memport: source holds %d groups (%s) — pick one; "+
			"importing several into one agent would merge separate conversations",
			len(candidates), strings.Join(candidates, ", "))
	}
}

// --- OKF --------------------------------------------------------------

func readOKF(fsys fs.FS, names []string, b *builder) error {
	for _, n := range names {
		if !strings.EqualFold(path.Ext(n), ".md") {
			b.skip(n, "not a markdown document")
			continue
		}
		raw, ok := readCapped(fsys, b, n)
		if !ok {
			continue
		}
		fm, body, err := parseFrontmatter(raw)
		if err != nil {
			// A header we cannot read is a reason to distrust the
			// metadata, not a reason to lose the document: the body is
			// somebody's memory. Keep it, tell the operator, and let the
			// path rules place it.
			b.skip(n, "frontmatter did not parse, importing the body without its metadata: "+err.Error())
			fm = frontmatter{}
		}

		rel := fm.CrewshipPath
		tier := tierFromOKFType(fm.Type)
		if rel == "" {
			rel = canonicalFileForTier(tier)
		}
		b.merge(rel, tier, scopeFromOKF(fm, rel, tier), n, body)
		b.annotate(rel, fm.Title, fm.Tags)
	}
	return nil
}

// tierFromOKFType maps an OKF `type` to a Crewship tier. A foreign
// bundle uses whatever vocabulary its author chose ("table", "metric",
// "api"), none of which we have storage for — those become agent-tier
// knowledge, which is the tier that means "things this agent knows".
func tierFromOKFType(t string) memory.Tier {
	switch memory.Tier(strings.ToLower(strings.TrimSpace(t))) {
	case memory.TierCrew:
		return memory.TierCrew
	case memory.TierWorkspace:
		return memory.TierWorkspace
	case memory.TierPins:
		return memory.TierPins
	case memory.TierLearned:
		return memory.TierLearned
	default:
		return memory.TierAgent
	}
}

func canonicalFileForTier(t memory.Tier) string {
	switch t {
	case memory.TierCrew:
		return "CREW.md"
	case memory.TierWorkspace:
		return "CREW.md"
	case memory.TierPins:
		return "pins.md"
	case memory.TierLearned:
		return "learned.md"
	default:
		return "AGENT.md"
	}
}

// --- Crewship ---------------------------------------------------------

func readCrewship(fsys fs.FS, names []string, b *builder, opts Options) error {
	for _, n := range names {
		if !strings.EqualFold(path.Ext(n), ".md") {
			b.skip(n, "not a markdown document")
			continue
		}
		body, ok := readCapped(fsys, b, n)
		if !ok {
			continue
		}
		// A live .memory tree is plain markdown and is passed through
		// byte for byte. It is NOT parsed for frontmatter: an agent's
		// note may legitimately open with a `---` thematic break, and
		// treating that as a YAML header either deletes the block or —
		// when it does not parse as YAML — dropped the whole file from
		// the export while the CLI reported success.
		//
		// Our own bundles are read by the OKF reader instead; Detect
		// routes them there on the okf.yaml manifest.
		rel := n
		tier := tierForCrewshipPath(rel)
		b.merge(rel, tier, scopeForCrewshipPath(rel, opts.DefaultScope), n, body)
	}
	return nil
}

// tierForCrewshipPath mirrors the mapping internal/memory's audit
// watcher applies to the live tree, so a file exported from a tier
// returns to the same one.
func tierForCrewshipPath(rel string) memory.Tier {
	switch base := path.Base(rel); {
	case strings.EqualFold(base, "CREW.md"):
		return memory.TierCrew
	case strings.EqualFold(base, "pins.md"):
		return memory.TierPins
	case strings.EqualFold(base, "lessons.md"), strings.EqualFold(base, "learned.md"):
		return memory.TierLearned
	case strings.HasPrefix(base, "learned-") && strings.HasSuffix(base, ".md"):
		// The consolidator's real filenames. audit_watcher.go maps the
		// same prefix; recognising only the bare "learned.md" stamped
		// tier "agent" on every crew rule file that left in an export.
		return memory.TierLearned
	default:
		return memory.TierAgent
	}
}

// --- builder ----------------------------------------------------------

// builder accumulates fragments per canonical path and renders them in
// insertion order. Insertion order (not map order) is what makes two
// runs over the same source produce identical bytes.
type builder struct {
	order     []string
	docs      map[string]*Doc
	fragments map[string][][]byte
	skipped   []Skip
}

func newBuilder() *builder {
	return &builder{docs: map[string]*Doc{}, fragments: map[string][][]byte{}}
}

func (b *builder) merge(rel string, tier memory.Tier, scope Scope, source string, body []byte) {
	if !validRelPath(rel) {
		b.skip(source, "would land at "+rel+", which is not a path inside a memory directory")
		return
	}
	d, ok := b.docs[rel]
	if !ok {
		d = &Doc{Tier: tier, Scope: scope, RelPath: rel}
		b.docs[rel] = d
		b.order = append(b.order, rel)
	}
	d.Sources = append(d.Sources, source)
	b.fragments[rel] = append(b.fragments[rel], body)
}

// annotate attaches metadata that only the first contributing source
// can supply. A merged document has no single title, so later sources
// do not overwrite an earlier one.
func (b *builder) annotate(rel, title string, tags []string) {
	d, ok := b.docs[rel]
	if !ok {
		return
	}
	if d.Title == "" {
		d.Title = title
	}
	if len(d.Tags) == 0 {
		d.Tags = tags
	}
}

func (b *builder) skip(source, reason string) {
	b.skipped = append(b.skipped, Skip{Source: source, Reason: reason})
}

func (b *builder) plan(f Format) Plan {
	p := Plan{Format: f, Skipped: b.skipped}
	for _, rel := range b.order {
		d := b.docs[rel]
		d.Body = renderFragments(d.Sources, b.fragments[rel])
		p.Docs = append(p.Docs, *d)
	}
	return p
}

// renderFragments joins the pieces that became one canonical file. A
// single source is passed through untouched — headings that nobody
// asked for are noise. Several sources each get a heading naming the
// file they came from, because "where did this sentence come from"
// is the first question asked of imported memory.
func renderFragments(sources []string, frags [][]byte) []byte {
	if len(frags) == 1 {
		return frags[0]
	}
	var buf bytes.Buffer
	for i, frag := range frags {
		if i > 0 {
			buf.WriteString("\n")
		}
		src := ""
		if i < len(sources) {
			src = sources[i]
		}
		fmt.Fprintf(&buf, "## %s\n\n", src)
		buf.Write(bytes.TrimRight(frag, "\n"))
		buf.WriteString("\n")
	}
	return buf.Bytes()
}

func topDir(p string) string {
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// scopeForCrewshipPath places a document read off a live tree. The
// caller supplies the scope of the directory it opened; the per-crew
// <slug>/topics/ subtree is crew-shared wherever it is found, which is
// what makes a crew export round-trip back into the crew.
func scopeForCrewshipPath(rel string, dflt Scope) Scope {
	if strings.Contains(rel, "/topics/") {
		return ScopeCrew
	}
	if strings.EqualFold(path.Base(rel), "CREW.md") {
		return ScopeCrew
	}
	if dflt == "" {
		return ScopeAgent
	}
	return dflt
}

// scopeFromOKF recovers the destination recorded in a bundle we wrote,
// falling back to the path/tier rules for a foreign one.
func scopeFromOKF(fm frontmatter, rel string, tier memory.Tier) Scope {
	switch Scope(strings.ToLower(strings.TrimSpace(fm.Scope))) {
	case ScopeCrew:
		return ScopeCrew
	case ScopeAgent:
		return ScopeAgent
	}
	if tier == memory.TierCrew || tier == memory.TierWorkspace {
		return ScopeCrew
	}
	return scopeForCrewshipPath(rel, ScopeAgent)
}
