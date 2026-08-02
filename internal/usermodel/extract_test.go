package usermodel

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// fakeProvider replies with a canned body and records what it was asked.
type fakeProvider struct {
	reply  string
	err    error
	seenIn llm.Request
	calls  int
}

func (f *fakeProvider) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	f.calls++
	f.seenIn = req
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Response{Content: f.reply}, nil
}

func (f *fakeProvider) Stream(context.Context, llm.Request, func(llm.StreamEvent) error) (*llm.Response, error) {
	panic("not used")
}
func (f *fakeProvider) Name() string { return "fake" }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seedTranscript writes a workspace, user, agent, chat and the given
// turns into conversation_messages the way the production mirror does.
func seedTranscript(t *testing.T, db *sql.DB, turns []struct {
	role, content, author string
}) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed (%s): %v", q, err)
		}
	}
	exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`)
	exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1','ws1','Crew','crew')`)
	exec(`INSERT INTO users (id, email) VALUES ('u1','u1@x')`)
	exec(`INSERT INTO users (id, email) VALUES ('u2','u2@x')`)
	exec(`INSERT INTO agents (id, workspace_id, crew_id, slug, name, agent_role)
	      VALUES ('a1','ws1','cr1','dev','Dev','AGENT')`)
	exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, message_count, started_at, visibility)
	      VALUES ('ch1','a1','ws1','u1',20,?,'group')`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))

	base := time.Now().UTC().Add(-time.Hour)
	for i, tr := range turns {
		var author any
		if tr.author != "" {
			author = tr.author
		}
		exec(`INSERT INTO conversation_messages (id, session_id, agent_id, role, content, ts, author_user_id)
		      VALUES (?, 'ch1', 'a1', ?, ?, ?, ?)`,
			"m"+string(rune('a'+i)), tr.role, tr.content,
			isoMillis(base.Add(time.Duration(i)*time.Minute)), author)
	}
}

// The end-to-end assertion for #1669: given a real conversation and a
// model that proposes both stated facts and inferences, exactly the
// stated ones reach the file.
func TestExtract_StoresOnlyWhatWasStated(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here, and the deploy pipeline is mine.", "u1"},
		{"assistant", "Understood. You seem pretty frustrated with the review tooling today.", ""},
		{"user", "One rule: commits must not carry a co-author trailer.", "u1"},
		{"user", "Pavel is basically the only person who understands billing.", "u2"},
	})

	prov := &fakeProvider{reply: `Here's what I found:
{"facts":[
  {"key":"role","value":"runs the platform team","quote":"I run the platform team here","source":"stated"},
  {"key":"owns","value":"the deploy pipeline","quote":"the deploy pipeline is mine","source":"stated"},
  {"key":"constraint","value":"commits carry no co-author trailer","quote":"commits must not carry a co-author trailer","source":"stated"},
  {"key":"prefers","value":"is frustrated by the review tooling","quote":"You seem pretty frustrated with the review tooling today.","source":"stated"},
  {"key":"tooling","value":"billing service ownership","quote":"Pavel is basically the only person who understands billing.","source":"stated"},
  {"key":"timezone","value":"probably Central European","quote":"","source":"inferred"}
]}`}

	ex := New(db, func() (llm.Provider, string) { return prov, "m" },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger())

	body, err := ex.Extract(context.Background(),
		consolidate.UserModelCandidate{WorkspaceID: "ws1", CrewID: "cr1", UserID: "u1"}, "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	mustHave := []string{
		"- role: runs the platform team",
		"- owns: the deploy pipeline",
		"- constraint: commits carry no co-author trailer",
	}
	for _, want := range mustHave {
		if !strings.Contains(body, want) {
			t.Errorf("stated fact missing from the model:\nwant line %q\ngot:\n%s", want, body)
		}
	}
	mustRefuse := map[string]string{
		"frustrated":       "sentiment the AGENT projected, quoted from an assistant turn",
		"billing":          "a third party's claim about the subject",
		"Central European": "an inference with no evidence at all",
		"probably":         "an inference with no evidence at all",
	}
	for frag, why := range mustRefuse {
		if strings.Contains(body, frag) {
			t.Errorf("%s reached the stored model (%q):\n%s", why, frag, body)
		}
	}
	// The file is bullet-shaped so consolidate.MergeUserModel can merge it
	// field-wise; anything else silently becomes unmergeable prose.
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "- ") || !strings.Contains(line, ": ") {
			t.Errorf("non-bullet line in the stored model: %q", line)
		}
	}
}

// The prior model is passed WHOLE. LangMem reconciles against only the
// top-5 retrieved facts, so a stored fact contradicting the new one is
// never seen when it falls outside that window; at nine fields there is
// no reason to reproduce that.
func TestExtract_PassesTheWholePriorModel(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here.", "u1"},
	})
	prior := "- timezone: UTC+1\n- language: Czech\n- tooling: Postgres and Go"
	prov := &fakeProvider{reply: `{"facts":[]}`}
	ex := New(db, func() (llm.Provider, string) { return prov, "m" },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger())

	if _, err := ex.Extract(context.Background(),
		consolidate.UserModelCandidate{WorkspaceID: "ws1", UserID: "u1"}, prior); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	sent := prov.seenIn.Messages[0].Content
	for _, line := range strings.Split(prior, "\n") {
		if !strings.Contains(sent, line) {
			t.Errorf("prior field %q was not shown to the model", line)
		}
	}
}

// A conversation the subject did not speak in is answered without paying
// a model: under a stated-only policy there is exactly one possible
// answer and it is "nothing".
func TestExtract_NoSubjectTurnsSkipsTheModelCall(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"assistant", "Anyone there?", ""},
		{"user", "Pavel is out this week.", "u2"},
	})
	prov := &fakeProvider{reply: `{"facts":[]}`}
	ex := New(db, func() (llm.Provider, string) { return prov, "m" },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger())

	body, err := ex.Extract(context.Background(),
		consolidate.UserModelCandidate{WorkspaceID: "ws1", UserID: "u1"}, "")
	if err != nil || body != "" {
		t.Fatalf("want empty/no error, got %q / %v", body, err)
	}
	if prov.calls != 0 {
		t.Errorf("model was called %d times for a transcript the subject never spoke in", prov.calls)
	}
}

// An unbuildable aux slot is "feature off", not an error — the sweep must
// keep doing its opt-out purge and its indexing.
func TestExtract_NilProviderIsFeatureOffNotAnError(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here.", "u1"},
	})
	ex := New(db, func() (llm.Provider, string) { return nil, "" },
		func(context.Context) Profile { return ProfileStatedTechnical }, quietLogger())
	body, err := ex.Extract(context.Background(),
		consolidate.UserModelCandidate{WorkspaceID: "ws1", UserID: "u1"}, "")
	if err != nil || body != "" {
		t.Fatalf("want empty/no error, got %q / %v", body, err)
	}
}

// The profile is read per extraction, so an operator switching it off is
// in force on the next sweep rather than the next server restart. #1606
// and #1556 are both this bug in other subsystems.
func TestExtract_ProfileIsReadPerCallNotCaptured(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	seedTranscript(t, db, []struct{ role, content, author string }{
		{"user", "I run the platform team here.", "u1"},
	})
	prov := &fakeProvider{reply: `{"facts":[{"key":"role","value":"runs the platform team","quote":"I run the platform team here","source":"stated"}]}`}
	current := ProfileStatedTechnical
	ex := New(db, func() (llm.Provider, string) { return prov, "m" },
		func(context.Context) Profile { return current }, quietLogger())
	cand := consolidate.UserModelCandidate{WorkspaceID: "ws1", UserID: "u1"}

	if body, _ := ex.Extract(context.Background(), cand, ""); body == "" {
		t.Fatal("expected a fact under the shipped profile")
	}
	current = ProfileOff
	body, err := ex.Extract(context.Background(), cand, "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if body != "" {
		t.Errorf("switching to the off profile did not take effect; got %q", body)
	}
	if prov.calls != 1 {
		t.Errorf("the off profile still called the model (%d calls)", prov.calls)
	}
}

// A malformed reply is an error, not "no facts today". Treating it as an
// empty extraction would make a broken extractor indistinguishable from a
// working one with nothing to say — which is exactly the state #1669 was
// filed about.
func TestParseCandidates_MalformedReplyIsAnError(t *testing.T) {
	for _, raw := range []string{"", "I could not find anything.", "{not json}"} {
		if _, err := ParseCandidates(raw); err == nil {
			t.Errorf("ParseCandidates(%q) returned no error", raw)
		}
	}
	got, err := ParseCandidates("```json\n{\"facts\":[]}\n```")
	if err != nil || len(got) != 0 {
		t.Errorf("a fenced empty result should parse cleanly; got %v / %v", got, err)
	}
}

func TestResolveProfile(t *testing.T) {
	if p, err := ResolveProfile(""); err != nil || p.Name != DefaultProfileName {
		t.Errorf("empty name should give the default; got %v / %v", p.Name, err)
	}
	if p, err := ResolveProfile("  STATED-TECHNICAL "); err != nil || p.Name != "stated-technical" {
		t.Errorf("name should be trimmed and case-folded; got %v / %v", p.Name, err)
	}
	if _, err := ResolveProfile("inferred"); err == nil {
		t.Error("an unregistered profile must be an error, not a silent fallback")
	}
}
