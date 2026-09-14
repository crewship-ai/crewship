package memdiff

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// §8 requires the golden fixtures to ship with the change: "Referenční
// implementace a golden fixtures jsou součást změny, server ji používá pro
// kontrolu removals." These files are the wire-level record of what this
// package decides. A change to the algorithm shows up here as a reviewable
// diff, in the same units the API speaks, rather than as a Go expression
// buried in a table.
//
// Regenerate with:
//
//	go test ./internal/memory/memdiff/ -run TestGolden -update
//
// and read the resulting diff line by line before committing it.
var updateGolden = flag.Bool("update", false, "rewrite the testdata golden files")

// goldenCase is the on-disk fixture format. Content is carried as strings so
// the files are readable; [][]byte would marshal as base64.
type goldenCase struct {
	Name     string        `json:"name"`
	Why      string        `json:"why"`
	Base     string        `json:"base"`
	Target   string        `json:"target"`
	Ops      []goldenOp    `json:"ops"`
	Removals []Removal     `json:"removals"`
	Applied  string        `json:"applied"`
	Verify   goldenVerify  `json:"verify"`
	Extra    []goldenVerdi `json:"rejected_declarations,omitempty"`
}

type goldenOp struct {
	Kind      string   `json:"kind"`
	StartLine int      `json:"start_line"`
	LineCount int      `json:"line_count"`
	Lines     []string `json:"lines"`
}

type goldenVerify struct {
	DerivedAccepted bool `json:"derived_removals_accepted"`
}

// goldenVerdi records a declaration that must be rejected, and with which
// error code. It is how a fixture pins the negative half of the contract.
type goldenVerdi struct {
	Why      string    `json:"why"`
	Declared []Removal `json:"declared"`
	Code     string    `json:"code"`
}

func goldenCases(t *testing.T) []goldenCase {
	t.Helper()
	return []goldenCase{
		{
			Name: "empty_to_empty",
			Why:  "Both sides empty: no ops, no removals. SplitLines(\"\") is zero lines, not one empty line.",
		},
		{
			Name:   "empty_base_create",
			Why:    "Creating a file from nothing is a single insert anchored at 0 (before the first base line) and removes nothing.",
			Target: "alpha\nbeta\n",
		},
		{
			Name: "delete_everything",
			Why:  "Emptying a file is one removal covering the whole base; the hash is over the whole file including its final LF.",
			Base: "alpha\nbeta\n",
		},
		{
			Name:   "pure_append",
			Why:    "The append path. §8 requires empty removals here, which is what makes 'append expressed as replace' legal without touching existing content.",
			Base:   "alpha\nbeta\n",
			Target: "alpha\nbeta\ngamma\n",
		},
		{
			Name:   "delete_at_start",
			Why:    "Removal spans start at line 1; numbering is 1-based.",
			Base:   "alpha\nbeta\ngamma\n",
			Target: "beta\ngamma\n",
		},
		{
			Name:   "delete_at_end",
			Why:    "The last line's hash includes its LF because this base ends in one.",
			Base:   "alpha\nbeta\ngamma\n",
			Target: "alpha\nbeta\n",
		},
		{
			Name:   "delete_in_middle",
			Why:    "Equal runs on both sides keep their own base line numbers.",
			Base:   "alpha\nbeta\ngamma\n",
			Target: "alpha\ngamma\n",
		},
		{
			Name:   "adjacent_deletions_merge",
			Why:    "§8: 'sloučit sousední deletions'. Three consecutive deleted lines are ONE op and ONE removal hashed over all three lines together.",
			Base:   "a\nb\nc\nd\ne\n",
			Target: "a\ne\n",
		},
		{
			Name:   "rewritten_line_is_delete_plus_insert",
			Why:    "§8: 'Přepsaný řádek je delete + insert.' There is no change op, and the delete precedes the insert.",
			Base:   "alpha\nbeta\ngamma\n",
			Target: "alpha\nBETA\ngamma\n",
		},
		{
			Name:   "tie_break_delete_before_insert",
			Why:    "A one-line file whose only line is replaced. Both 'delete then insert' and 'insert then delete' are shortest scripts of length 2; §8 fixes delete first, and Myers' tie-break delivers it.",
			Base:   "old\n",
			Target: "new\n",
		},
		{
			Name: "repeated_lines_abab",
			Why: "§8: 'Opakované řádky určuje jejich pozice v původní revizi.' base a,b,a,b,a -> target a,b,a has THREE shortest scripts: delete lines {2,3}, {3,4} or {4,5}. " +
				"The deterministic answer is {4,5}. Myers' greedy phase extends the diagonal as far as it can before spending an edit, so it matches base lines 1,2,3 against the entire target first and the frontier is already at (3,3); " +
				"the only way on to (5,3) is two delete moves, which merge into a single span. The rule to remember: the EARLIEST occurrences in the base are kept.",
			Base:   "a\nb\na\nb\na\n",
			Target: "a\nb\na\n",
		},
		{
			Name:   "repeated_lines_keep_prefix",
			Why:    "The same principle when the target is a suffix of the repeats: base a,a,a -> target a,a keeps lines 1,2 and deletes line 3.",
			Base:   "a\na\na\n",
			Target: "a\na\n",
		},
		{
			Name:   "crlf_input",
			Why:    "CRLF input normalizes to LF before anything else happens, so this fixture's ops, removals and hashes are byte-identical to lf_input below.",
			Base:   "alpha\r\nbeta\r\ngamma\r\n",
			Target: "alpha\r\ngamma\r\n",
		},
		{
			Name:   "lf_input",
			Why:    "The LF twin of crlf_input. If these two fixtures ever diverge, normalization broke.",
			Base:   "alpha\nbeta\ngamma\n",
			Target: "alpha\ngamma\n",
		},
		{
			Name:   "trailing_newline_present",
			Why:    "Last line 'gamma\\n' is hashed WITH its LF. Compare trailing_newline_absent: same visible line, different hash.",
			Base:   "alpha\nbeta\ngamma\n",
			Target: "alpha\nbeta\n",
		},
		{
			Name:   "trailing_newline_absent",
			Why:    "Last line 'gamma' is hashed WITHOUT an LF, because the base has none. §8: 'poslední řádek LF nemít může.'",
			Base:   "alpha\nbeta\ngamma",
			Target: "alpha\nbeta\n",
		},
		{
			Name:   "append_to_unterminated_base",
			Why:    "Appending to a file with no trailing newline necessarily REWRITES its last line ('beta' -> 'beta\\n'), so it is a delete plus an insert and removals are not empty. This is why append must not silently add the LF.",
			Base:   "alpha\nbeta",
			Target: "alpha\nbeta\ngamma\n",
		},
		{
			Name:   "unicode_lines",
			Why:    "Multi-byte content. Lines are compared and hashed as bytes; the removal hash covers the UTF-8 encoding of the line plus its LF.",
			Base:   "příliš\nžluťoučký\nkůň\n🚀\n",
			Target: "příliš\nkůň\n",
		},
		{
			Name:   "bare_cr_is_content",
			Why:    "A CR that is not part of a CRLF stays inside its line, so 'a\\rb' and 'ab' are different lines and this is a rewrite, not an equal.",
			Base:   "a\rb\nc\n",
			Target: "ab\nc\n",
		},
		{
			Name:   "blank_lines",
			Why:    "Blank lines are ordinary lines. Of two identical blank lines the later one is deleted, for the same positional reason as repeated_lines_abab.",
			Base:   "a\n\n\nb\n",
			Target: "a\n\nb\n",
		},
		{
			Name:   "markdown_memory_edit",
			Why:    "A realistic memory-file edit: a heading kept, one bullet rewritten, two consecutive bullets dropped, one appended.",
			Base:   "# Lessons\n\n- always claim the issue first\n- never merge on red CI\n- the DB is repo-local\n- run tests in the foreground\n",
			Target: "# Lessons\n\n- always claim the issue before the first commit\n- run tests in the foreground\n- wait for CodeRabbit\n",
		},
	}
}

func TestGolden(t *testing.T) {
	dir := filepath.Join("testdata", "golden")
	if *updateGolden {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	for _, tc := range goldenCases(t) {
		t.Run(tc.name(), func(t *testing.T) {
			got := build(t, tc)
			path := filepath.Join(dir, tc.Name+".json")

			encoded, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			encoded = append(encoded, '\n')

			if *updateGolden {
				if err := os.WriteFile(path, encoded, 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				t.Logf("wrote %s", path)
				return
			}

			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s (regenerate with -update): %v", path, err)
			}
			if string(encoded) != string(want) {
				t.Fatalf("golden %s is stale.\n--- want (on disk) ---\n%s\n--- got (computed) ---\n%s",
					path, want, encoded)
			}

			// The fixture is not just a snapshot: replay its recorded
			// verdicts against the live code.
			replay(t, got)
		})
	}
}

func (c goldenCase) name() string { return c.Name }

// build recomputes a fixture from its base/target. Everything below the two
// inputs is derived, so a fixture can never disagree with itself.
func build(t *testing.T, tc goldenCase) goldenCase {
	t.Helper()
	base := lines(t, tc.Base)
	target := lines(t, tc.Target)

	out := goldenCase{Name: tc.Name, Why: tc.Why, Base: tc.Base, Target: tc.Target}

	ops := Diff(base, target)
	out.Ops = make([]goldenOp, 0, len(ops))
	for _, op := range ops {
		out.Ops = append(out.Ops, goldenOp{
			Kind:      op.Kind.String(),
			StartLine: op.StartLine,
			LineCount: op.LineCount,
			Lines:     asStrings(op.Lines),
		})
	}

	out.Removals = Removals(base, target)
	if out.Removals == nil {
		out.Removals = []Removal{}
	}

	applied, err := Apply(base, ops)
	if err != nil {
		t.Fatalf("%s: Apply: %v", tc.Name, err)
	}
	for _, l := range applied {
		out.Applied += string(l)
	}

	out.Verify.DerivedAccepted = VerifyRemovals(base, target, out.Removals) == nil

	// Every fixture that removes something also pins the two rejections that
	// matter most: dropping the declaration, and corrupting its hash.
	if len(out.Removals) > 0 {
		out.Extra = append(out.Extra, goldenVerdi{
			Why:      "declaring nothing while the diff removes lines",
			Declared: []Removal{},
			Code:     codeOf(t, base, target, nil),
		})
		bad := append([]Removal(nil), out.Removals...)
		bad[0].OldSHA256 = "00000000000000000000000000000000000000000000000000000000000000ff"
		out.Extra = append(out.Extra, goldenVerdi{
			Why:      "a declaration whose old_sha256 does not match the base",
			Declared: bad,
			Code:     codeOf(t, base, target, bad),
		})
	}
	return out
}

func codeOf(t *testing.T, base, target [][]byte, declared []Removal) string {
	t.Helper()
	err := VerifyRemovals(base, target, declared)
	if err == nil {
		t.Fatalf("expected VerifyRemovals to reject %+v", declared)
	}
	var re *RemovalError
	if !asRemovalError(err, &re) {
		t.Fatalf("error %v is not a *RemovalError", err)
	}
	return re.Code
}

// replay re-executes the recorded verdicts so the fixture is a live assertion
// rather than a snapshot of whatever the code happened to do.
func replay(t *testing.T, c goldenCase) {
	t.Helper()
	base, target := lines(t, c.Base), lines(t, c.Target)

	if err := VerifyRemovals(base, target, c.Removals); (err == nil) != c.Verify.DerivedAccepted {
		t.Fatalf("derived_removals_accepted=%v but VerifyRemovals returned %v", c.Verify.DerivedAccepted, err)
	}
	if !c.Verify.DerivedAccepted {
		t.Fatal("a derived removal set must always verify against its own base and target")
	}
	for _, v := range c.Extra {
		if got := codeOf(t, base, target, v.Declared); got != v.Code {
			t.Fatalf("%s: code = %q, want %q", v.Why, got, v.Code)
		}
	}
	// The applied text must equal the (normalized) target text.
	normTarget, err := Normalize([]byte(c.Target))
	if err != nil {
		t.Fatalf("Normalize target: %v", err)
	}
	if c.Applied != string(normTarget) {
		t.Fatalf("applied = %q, want the normalized target %q", c.Applied, normTarget)
	}
}

// TestGoldenCRLFTwins reads the two twin fixtures off disk and asserts they
// agree on everything except their raw inputs. This is the CRLF requirement
// stated at the fixture level: if normalization ever changes, the two files
// diverge and this fails even if both files were regenerated together.
func TestGoldenCRLFTwins(t *testing.T) {
	crlf := readGolden(t, "crlf_input")
	lf := readGolden(t, "lf_input")

	if crlf.Base == lf.Base {
		t.Fatal("the twin fixtures have identical raw bases; the CRLF one is not testing anything")
	}
	if got, want := jsonOf(t, crlf.Ops), jsonOf(t, lf.Ops); got != want {
		t.Fatalf("CRLF ops differ from LF ops:\n%s\n%s", got, want)
	}
	if got, want := jsonOf(t, crlf.Removals), jsonOf(t, lf.Removals); got != want {
		t.Fatalf("CRLF removals differ from LF removals:\n%s\n%s", got, want)
	}
	if crlf.Applied != lf.Applied {
		t.Fatalf("applied text differs: %q vs %q", crlf.Applied, lf.Applied)
	}
}

// TestGoldenTrailingNewlineTwins asserts the opposite: the same visible last
// line must NOT hash the same when the base's final newline differs.
func TestGoldenTrailingNewlineTwins(t *testing.T) {
	present := readGolden(t, "trailing_newline_present")
	absent := readGolden(t, "trailing_newline_absent")

	if len(present.Removals) != 1 || len(absent.Removals) != 1 {
		t.Fatalf("expected one removal each, got %d and %d", len(present.Removals), len(absent.Removals))
	}
	p, a := present.Removals[0], absent.Removals[0]
	if p.StartLine != a.StartLine || p.LineCount != a.LineCount {
		t.Fatalf("the twins should cover the same span: %+v vs %+v", p, a)
	}
	if p.OldSHA256 == a.OldSHA256 {
		t.Fatal("a last line with an LF and one without must not hash the same")
	}
	if want := shaOf("gamma\n"); p.OldSHA256 != want {
		t.Errorf("terminated last line hash = %s, want sha256(\"gamma\\n\") = %s", p.OldSHA256, want)
	}
	if want := shaOf("gamma"); a.OldSHA256 != want {
		t.Errorf("unterminated last line hash = %s, want sha256(\"gamma\") = %s", a.OldSHA256, want)
	}
}

func readGolden(t *testing.T, name string) goldenCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", name+".json"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var c goldenCase
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse golden %s: %v", name, err)
	}
	return c
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
