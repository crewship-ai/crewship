package memport

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/crewship-ai/crewship/internal/memory"
)

// docFor finds the produced document for a canonical Crewship path.
func docFor(t *testing.T, plan Plan, rel string) Doc {
	t.Helper()
	for _, d := range plan.Docs {
		if d.RelPath == rel {
			return d
		}
	}
	t.Fatalf("no document produced for %q; got %v", rel, relPaths(plan))
	return Doc{}
}

func relPaths(plan Plan) []string {
	out := make([]string, 0, len(plan.Docs))
	for _, d := range plan.Docs {
		out = append(out, d.RelPath)
	}
	return out
}

func TestReadOpenClaw(t *testing.T) {
	fsys := fstest.MapFS{
		"SOUL.md":              &fstest.MapFile{Data: []byte("Be terse.")},
		"IDENTITY.md":          &fstest.MapFile{Data: []byte("You are Ada.")},
		"MEMORY.md":            &fstest.MapFile{Data: []byte("The deploy key rotates monthly.")},
		"AGENTS.md":            &fstest.MapFile{Data: []byte("Ada leads, Bob reviews.")},
		"USER.md":              &fstest.MapFile{Data: []byte("Prefers Czech.")},
		"memory/2026-02-13.md": &fstest.MapFile{Data: []byte("Shipped the parser.")},
		"memory/projects.md":   &fstest.MapFile{Data: []byte("Crewship 1.0 is the current push.")},
		"memory/decisions.md":  &fstest.MapFile{Data: []byte("Chose SQLite.")},
		// Derived data: never imported.
		"vectors/index.bin":        &fstest.MapFile{Data: []byte("\x00\x01")},
		"sessions/abc/turns.jsonl": &fstest.MapFile{Data: []byte("{}\n")},
	}

	plan, err := ReadSource(fsys, FormatOpenClaw, Options{OperatorSlug: "pavel"})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}

	// SOUL + IDENTITY are both "how to behave" — they land in PERSONA.md,
	// each under a heading naming where it came from, so a human reading
	// the result can tell the two apart afterwards.
	persona := docFor(t, plan, "PERSONA.md")
	if persona.Tier != memory.TierAgent {
		t.Errorf("PERSONA.md tier = %q, want %q", persona.Tier, memory.TierAgent)
	}
	for _, want := range []string{"Be terse.", "You are Ada.", "SOUL.md", "IDENTITY.md"} {
		if !strings.Contains(string(persona.Body), want) {
			t.Errorf("PERSONA.md missing %q; got:\n%s", want, persona.Body)
		}
	}

	// MEMORY.md plus the non-dated topic files are all long-term agent
	// knowledge; they merge into AGENT.md rather than inventing tiers we
	// have no storage for.
	agent := docFor(t, plan, "AGENT.md")
	for _, want := range []string{"deploy key rotates", "Crewship 1.0", "Chose SQLite"} {
		if !strings.Contains(string(agent.Body), want) {
			t.Errorf("AGENT.md missing %q; got:\n%s", want, agent.Body)
		}
	}

	crew := docFor(t, plan, "CREW.md")
	if crew.Tier != memory.TierCrew {
		t.Errorf("CREW.md tier = %q, want %q", crew.Tier, memory.TierCrew)
	}

	daily := docFor(t, plan, "daily/2026-02-13.md")
	if got := string(daily.Body); !strings.Contains(got, "Shipped the parser.") {
		t.Errorf("daily body = %q", got)
	}

	peer := docFor(t, plan, "peers/pavel.md")
	if got := string(peer.Body); !strings.Contains(got, "Prefers Czech.") {
		t.Errorf("peer card body = %q", got)
	}

	// Embeddings and transcripts are reported as skipped, not silently
	// dropped — "it imported fine" must not hide half the source tree.
	var skippedVectors, skippedSessions bool
	for _, s := range plan.Skipped {
		if strings.HasPrefix(s.Source, "vectors/") {
			skippedVectors = true
		}
		if strings.HasPrefix(s.Source, "sessions/") {
			skippedSessions = true
		}
	}
	if !skippedVectors || !skippedSessions {
		t.Errorf("derived data not reported as skipped: %+v", plan.Skipped)
	}
}

// Without an operator slug there is no peer card to write to. The file
// is skipped with a reason rather than guessed into peers/user.md.
func TestReadOpenClawWithoutOperatorSkipsUserCard(t *testing.T) {
	fsys := fstest.MapFS{
		"MEMORY.md": &fstest.MapFile{Data: []byte("x")},
		"USER.md":   &fstest.MapFile{Data: []byte("Prefers Czech.")},
	}
	plan, err := ReadSource(fsys, FormatOpenClaw, Options{})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}
	for _, d := range plan.Docs {
		if strings.HasPrefix(d.RelPath, "peers/") {
			t.Fatalf("USER.md became %q with no operator slug supplied", d.RelPath)
		}
	}
	var found bool
	for _, s := range plan.Skipped {
		if s.Source == "USER.md" {
			found = true
			if s.Reason == "" {
				t.Error("USER.md skipped with an empty reason")
			}
		}
	}
	if !found {
		t.Errorf("USER.md neither imported nor reported skipped: %+v", plan.Skipped)
	}
}

func TestReadNanoClaw(t *testing.T) {
	fsys := fstest.MapFS{
		"groups/global/CLAUDE.md":               &fstest.MapFile{Data: []byte("Always answer in Czech.")},
		"groups/telegram_dev-team/CLAUDE.md":    &fstest.MapFile{Data: []byte("This group ships the API.")},
		"groups/telegram_dev-team/notes.md":     &fstest.MapFile{Data: []byte("Standup is 09:30.")},
		"groups/telegram_dev-team/logs/run.txt": &fstest.MapFile{Data: []byte("noise")},
	}

	plan, err := ReadSource(fsys, FormatNanoClaw, Options{Group: "telegram_dev-team"})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}

	crew := docFor(t, plan, "CREW.md")
	if !strings.Contains(string(crew.Body), "Always answer in Czech.") {
		t.Errorf("global memory did not land in CREW.md; got:\n%s", crew.Body)
	}

	agent := docFor(t, plan, "AGENT.md")
	for _, want := range []string{"This group ships the API.", "Standup is 09:30.", "notes.md"} {
		if !strings.Contains(string(agent.Body), want) {
			t.Errorf("AGENT.md missing %q; got:\n%s", want, agent.Body)
		}
	}

	// Task logs are not memory.
	for _, d := range plan.Docs {
		if strings.Contains(string(d.Body), "noise") {
			t.Errorf("task log content leaked into %q", d.RelPath)
		}
	}
}

// Picking a group is required as soon as the source holds more than
// one: merging two groups' memory into a single agent silently mixes
// two separate contexts, which is the failure this import exists to
// avoid.
func TestReadNanoClawAmbiguousGroup(t *testing.T) {
	fsys := fstest.MapFS{
		"groups/global/CLAUDE.md":          &fstest.MapFile{Data: []byte("shared")},
		"groups/whatsapp_family/CLAUDE.md": &fstest.MapFile{Data: []byte("a")},
		"groups/telegram_devs/CLAUDE.md":   &fstest.MapFile{Data: []byte("b")},
	}
	_, err := ReadSource(fsys, FormatNanoClaw, Options{})
	if err == nil {
		t.Fatal("ReadSource() with two candidate groups and no --group returned nil error")
	}
	if !strings.Contains(err.Error(), "whatsapp_family") || !strings.Contains(err.Error(), "telegram_devs") {
		t.Errorf("error should name the candidates so the operator can pick; got: %v", err)
	}
}

// A single non-global group needs no disambiguation.
func TestReadNanoClawSingleGroupNeedsNoFlag(t *testing.T) {
	fsys := fstest.MapFS{
		"groups/global/CLAUDE.md":        &fstest.MapFile{Data: []byte("shared")},
		"groups/telegram_devs/CLAUDE.md": &fstest.MapFile{Data: []byte("b")},
	}
	plan, err := ReadSource(fsys, FormatNanoClaw, Options{})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}
	if !strings.Contains(string(docFor(t, plan, "AGENT.md").Body), "b") {
		t.Error("single group was not imported")
	}
}

func TestReadOKFRoundTrip(t *testing.T) {
	fsys := fstest.MapFS{
		"agent.md": &fstest.MapFile{Data: []byte(
			"---\ntype: agent\ntitle: Long-term\ntags:\n  - ops\n---\n\nThe deploy key rotates monthly.\n")},
		"daily-2026-08-01.md": &fstest.MapFile{Data: []byte(
			"---\ntype: agent\ncrewship_path: daily/2026-08-01.md\n---\n\nShipped it.\n")},
	}

	plan, err := ReadSource(fsys, FormatOKF, Options{})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}

	// An OKF bundle we wrote carries crewship_path and must land back
	// exactly where it came from.
	daily := docFor(t, plan, "daily/2026-08-01.md")
	if got := strings.TrimSpace(string(daily.Body)); got != "Shipped it." {
		t.Errorf("frontmatter leaked into the body: %q", got)
	}

	agent := docFor(t, plan, "AGENT.md")
	if agent.Title != "Long-term" {
		t.Errorf("Title = %q, want %q", agent.Title, "Long-term")
	}
	if len(agent.Tags) != 1 || agent.Tags[0] != "ops" {
		t.Errorf("Tags = %v, want [ops]", agent.Tags)
	}
}

// A LIVE memory tree is plain markdown. An agent that opens AGENT.md
// with a thematic break must not have that block eaten as YAML — and
// must never have the whole file dropped because the block did not
// parse. Exports that silently omit an agent's main memory file are
// worse than exports that fail.
func TestReadCrewshipDoesNotEatMarkdownRules(t *testing.T) {
	body := "---\nDeploy key rotates monthly.\n---\nRest of memory.\n"
	fsys := fstest.MapFS{
		"AGENT.md": &fstest.MapFile{Data: []byte(body)},
	}
	plan, err := ReadSource(fsys, FormatCrewship, Options{})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}
	if len(plan.Docs) != 1 {
		t.Fatalf("Docs = %v, skipped = %+v; want AGENT.md preserved", relPaths(plan), plan.Skipped)
	}
	if got := string(plan.Docs[0].Body); got != body {
		t.Errorf("body was rewritten:\n want %q\n  got %q", body, got)
	}
}

// The consolidator writes learned-<topic>.md, not learned.md. The audit
// watcher already knows that; the exporter must agree or the crew's
// consolidated rules leave with the wrong tier stamped on them.
func TestTierForCrewshipPathMatchesTheAuditWatcher(t *testing.T) {
	tests := []struct {
		rel  string
		want memory.Tier
	}{
		{"AGENT.md", memory.TierAgent},
		{"CREW.md", memory.TierCrew},
		{"pins.md", memory.TierPins},
		{"lessons.md", memory.TierLearned},
		{"learned.md", memory.TierLearned},
		{"eng/topics/learned-ops.md", memory.TierLearned},
		{"eng/topics/pins.md", memory.TierPins},
		{"daily/2026-08-01.md", memory.TierAgent},
	}
	for _, tt := range tests {
		if got := tierForCrewshipPath(tt.rel); got != tt.want {
			t.Errorf("tierForCrewshipPath(%q) = %q, want %q", tt.rel, got, tt.want)
		}
	}
}

// A file the reader classifies as "never imported" must not be able to
// abort the import by being unreadable — a dead symlink or a socket
// under sessions/ is ordinary on a real machine.
func TestReadOpenClawSurvivesUnreadableDerivedData(t *testing.T) {
	fsys := failingFS{
		MapFS: fstest.MapFS{
			"MEMORY.md":                &fstest.MapFile{Data: []byte("knowledge")},
			"sessions/abc/turns.jsonl": &fstest.MapFile{Data: []byte("{}")},
			"vectors/index.bin":        &fstest.MapFile{Data: []byte("\x00")},
		},
		failPrefixes: []string{"sessions/", "vectors/"},
	}
	plan, err := ReadSource(fsys, FormatOpenClaw, Options{})
	if err != nil {
		t.Fatalf("ReadSource() error = %v; an unreadable skipped file must not abort the import", err)
	}
	if _, ok := findDoc(plan, "AGENT.md"); !ok {
		t.Errorf("AGENT.md missing; got %v", relPaths(plan))
	}
}

func findDoc(plan Plan, rel string) (Doc, bool) {
	for _, d := range plan.Docs {
		if d.RelPath == rel {
			return d, true
		}
	}
	return Doc{}, false
}

// failingFS refuses to open anything under failPrefixes, standing in for
// a dead symlink, a socket, or a permission-denied transcript.
type failingFS struct {
	fstest.MapFS
	failPrefixes []string
}

func (f failingFS) Open(name string) (fs.File, error) {
	for _, p := range f.failPrefixes {
		if strings.HasPrefix(name, p) {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
		}
	}
	return f.MapFS.Open(name)
}

// Scope, not tier, decides which directory a document belongs to. A
// crew's pins are crew-scoped even though their tier is "pins", and an
// agent's own pins are not.
func TestReadNanoClawAssignsScope(t *testing.T) {
	fsys := fstest.MapFS{
		"groups/global/CLAUDE.md":   &fstest.MapFile{Data: []byte("shared")},
		"groups/telegram/CLAUDE.md": &fstest.MapFile{Data: []byte("mine")},
	}
	plan, err := ReadSource(fsys, FormatNanoClaw, Options{})
	if err != nil {
		t.Fatalf("ReadSource() error = %v", err)
	}
	crew, _ := findDoc(plan, "CREW.md")
	if crew.Scope != ScopeCrew {
		t.Errorf("CREW.md scope = %q, want %q", crew.Scope, ScopeCrew)
	}
	agent, _ := findDoc(plan, "AGENT.md")
	if agent.Scope != ScopeAgent {
		t.Errorf("AGENT.md scope = %q, want %q", agent.Scope, ScopeAgent)
	}
}
