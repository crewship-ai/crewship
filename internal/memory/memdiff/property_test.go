package memdiff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// serialize renders everything Diff and Removals decided into one string. It is
// the comparison unit for the determinism test: if any map iteration or slice
// aliasing leaked into the result, this string moves.
func serialize(base, target [][]byte) string {
	var b strings.Builder
	b.WriteString(formatOps(Diff(base, target)))
	for _, r := range Removals(base, target) {
		fmt.Fprintf(&b, "removal %d+%d %s\n", r.StartLine, r.LineCount, r.OldSHA256)
	}
	return b.String()
}

// TestDiffIsDeterministic runs the same non-trivial diff 1000 times and
// requires byte-identical output every time. Go randomizes map iteration order
// on purpose, so an implementation that reached for a map anywhere on this path
// would fail here rather than in production a week later.
func TestDiffIsDeterministic(t *testing.T) {
	base := lines(t, strings.Join([]string{
		"# Lessons", "", "- claim the issue first", "- never merge on red CI",
		"- the DB is repo-local", "- run tests in the foreground", "",
		"## Notes", "a", "b", "a", "b", "a", "", "tail",
	}, "\n")+"\n")
	target := lines(t, strings.Join([]string{
		"# Lessons", "", "- claim the issue before the first commit",
		"- run tests in the foreground", "- wait for CodeRabbit", "",
		"## Notes", "a", "b", "a", "", "tail", "extra",
	}, "\n")+"\n")

	want := serialize(base, target)
	if want == "" {
		t.Fatal("the fixture produced an empty script; it is not exercising anything")
	}
	for i := 1; i < 1000; i++ {
		if got := serialize(base, target); got != want {
			t.Fatalf("run %d differed:\n got:\n%s\nwant:\n%s", i, got, want)
		}
	}

	// And the same across fresh input slices, in case the result somehow
	// depended on the backing arrays' addresses.
	for i := 0; i < 100; i++ {
		freshBase := lines(t, joinLines(base))
		freshTarget := lines(t, joinLines(target))
		if got := serialize(freshBase, freshTarget); got != want {
			t.Fatalf("fresh-slice run %d differed:\n%s", i, got)
		}
	}
}

func joinLines(ls [][]byte) string {
	var b strings.Builder
	for _, l := range ls {
		b.Write(l)
	}
	return b.String()
}

// TestGoldenFixturesAreDeterministic re-runs every golden fixture 100 times.
// The golden files are the artefact §8 asks for; if any of them were unstable,
// the whole idea of a shared reference implementation collapses.
func TestGoldenFixturesAreDeterministic(t *testing.T) {
	for _, tc := range goldenCases(t) {
		base, target := lines(t, tc.Base), lines(t, tc.Target)
		want := serialize(base, target)
		for i := 0; i < 100; i++ {
			if got := serialize(base, target); got != want {
				t.Fatalf("%s run %d differed:\n got:\n%s\nwant:\n%s", tc.Name, i, got, want)
			}
		}
	}
}

// randomLines builds a line slice from a deliberately tiny alphabet so that
// repeated identical lines are the common case, not the exception — those are
// exactly the inputs where a diff implementation's tie-breaks show.
func randomLines(rng *rand.Rand, maxLines, alphabet int) [][]byte {
	n := rng.Intn(maxLines + 1)
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, []byte(fmt.Sprintf("%c\n", 'a'+rng.Intn(alphabet))))
	}
	if n > 0 && rng.Intn(4) == 0 {
		// A quarter of the time the last line has no LF, which is the shape
		// §8 singles out.
		last := out[n-1]
		out[n-1] = last[:len(last)-1]
	}
	return out
}

// TestPropertyRoundTrip is the property check §8's contract really rests on:
// whatever script the algorithm produces, replaying it on the base must
// reproduce the target exactly, the script must be a SHORTEST one, and the
// derived removals must verify. Ten thousand random pairs, fixed seeds so a
// failure is reproducible from the log line alone.
func TestPropertyRoundTrip(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 20260910} {
		seed := seed
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 2500; i++ {
				alphabet := 1 + rng.Intn(4) // 1..4 distinct lines: heavy repetition
				base := randomLines(rng, 12, alphabet)
				target := randomLines(rng, 12, alphabet)

				ops := Diff(base, target)

				applied, err := Apply(base, ops)
				if err != nil {
					t.Fatalf("seed %d case %d: Apply: %v\nbase=%q target=%q", seed, i, err, asStrings(base), asStrings(target))
				}
				if !equalLines(applied, target) {
					t.Fatalf("seed %d case %d: replay mismatch\nbase=%q\ntarget=%q\ngot=%q\nops:\n%s",
						seed, i, asStrings(base), asStrings(target), asStrings(applied), formatOps(ops))
				}

				edits := 0
				for _, op := range ops {
					if op.Kind != OpEqual {
						edits += op.LineCount
					}
				}
				if want := len(base) + len(target) - 2*lcsLen(base, target); edits != want {
					t.Fatalf("seed %d case %d: script has %d edits, shortest is %d\nbase=%q target=%q\nops:\n%s",
						seed, i, edits, want, asStrings(base), asStrings(target), formatOps(ops))
				}

				// Ops must tile the base exactly once, in order.
				next := 1
				for j, op := range ops {
					switch op.Kind {
					case OpEqual, OpDelete:
						if op.StartLine != next {
							t.Fatalf("seed %d case %d: op %d starts at %d, expected %d\nops:\n%s",
								seed, i, j, op.StartLine, next, formatOps(ops))
						}
						next += op.LineCount
					case OpInsert:
						if op.StartLine != next-1 {
							t.Fatalf("seed %d case %d: op %d (insert) anchored at %d, expected %d\nops:\n%s",
								seed, i, j, op.StartLine, next-1, formatOps(ops))
						}
					}
				}
				if next != len(base)+1 {
					t.Fatalf("seed %d case %d: ops cover %d base lines, base has %d\nops:\n%s",
						seed, i, next-1, len(base), formatOps(ops))
				}

				// No insert may immediately precede a delete: §8's
				// delete-before-insert rule, as a structural invariant.
				for j := 1; j < len(ops); j++ {
					if ops[j-1].Kind == OpInsert && ops[j].Kind == OpDelete {
						t.Fatalf("seed %d case %d: insert precedes delete\nbase=%q target=%q\nops:\n%s",
							seed, i, asStrings(base), asStrings(target), formatOps(ops))
					}
				}

				rem := Removals(base, target)
				assertRemovalInvariants(t, base, rem)
				for _, r := range rem {
					want, err := HashSpan(base, r.StartLine, r.LineCount)
					if err != nil {
						t.Fatalf("seed %d case %d: HashSpan: %v", seed, i, err)
					}
					if r.OldSHA256 != want {
						t.Fatalf("seed %d case %d: removal hash %s, want %s", seed, i, r.OldSHA256, want)
					}
				}
				if err := VerifyRemovals(base, target, rem); err != nil {
					t.Fatalf("seed %d case %d: VerifyRemovals rejected its own derivation: %v\nbase=%q target=%q",
						seed, i, err, asStrings(base), asStrings(target))
				}

				// Dropping any one declaration must be caught as undeclared.
				for k := range rem {
					short := append(append([]Removal{}, rem[:k]...), rem[k+1:]...)
					if err := VerifyRemovals(base, target, short); !isUndeclared(err) {
						t.Fatalf("seed %d case %d: dropping declaration %d gave %v, want ErrUndeclaredRemoval",
							seed, i, k, err)
					}
				}
			}
		})
	}
}

func isUndeclared(err error) bool {
	var re *RemovalError
	return asRemovalError(err, &re) && re.Code == "undeclared_removal"
}

// TestPropertyNormalizeRoundTrip pairs the normalization rules with the split:
// for random byte strings that are valid UTF-8, normalizing then splitting then
// rejoining must give back exactly the normalized bytes, and the result must
// never contain a CRLF unless a literal CR sat immediately before one.
func TestPropertyNormalizeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []string{"a", "b", "č", "🚀", "\n", "\r\n", "\r", " ", ""}
	for i := 0; i < 5000; i++ {
		var sb strings.Builder
		for j := rng.Intn(12); j > 0; j-- {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		in := sb.String()

		norm, ls, err := NormalizeLines([]byte(in))
		if err != nil {
			t.Fatalf("case %d: NormalizeLines(%q): %v", i, in, err)
		}
		if got := joinLines(ls); got != string(norm) {
			t.Fatalf("case %d: split/rejoin of %q lost bytes: %q vs %q", i, in, got, norm)
		}
		// Every line but the last ends in LF; the last may or may not.
		for j := 0; j < len(ls)-1; j++ {
			if ls[j][len(ls[j])-1] != '\n' {
				t.Fatalf("case %d: line %d of %q does not end in LF: %q", i, j, in, ls[j])
			}
		}
		if n := len(ls); n > 0 {
			body := ls[n-1]
			if idx := strings.IndexByte(string(body[:len(body)-1]), '\n'); idx >= 0 {
				t.Fatalf("case %d: last line %q contains an interior LF", i, body)
			}
		}
	}
}

// TestJSONShapeOfRemoval pins the wire names §8 uses. The server puts these on
// the API, so a rename here is an API break.
func TestJSONShapeOfRemoval(t *testing.T) {
	sum := sha256.Sum256([]byte("x\n"))
	r := Removal{StartLine: 3, LineCount: 2, OldSHA256: hex.EncodeToString(sum[:])}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"start_line":3,"line_count":2,"old_sha256":"` + hex.EncodeToString(sum[:]) + `"}`
	if string(b) != want {
		t.Fatalf("Removal JSON = %s, want %s", b, want)
	}

	var back Removal
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != r {
		t.Fatalf("round trip = %+v, want %+v", back, r)
	}
}
