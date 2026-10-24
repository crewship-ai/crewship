package api

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/crewship-ai/crewship/internal/config"
)

func testDiffBudget(bytes, lines int) *diffComputeBudget {
	return newDiffComputeBudget(config.DiffConfig{MaxInputBytes: bytes, MaxLines: lines, MaxConcurrent: 1})
}

func TestDiffBudgetPrettyEncodingAndExactBoundaries(t *testing.T) {
	for _, raw := range []string{`null`, `42`, `"<&>"`, `{}`, `[]`, `{"z":[],"a":{}}`, `{"s":"\\\"{},:[x]","a":[1,{"b":true}]}`, `["é","K","ſ"]`, "not JSON", "{bad\nJSON}", "1e999", "\xff"} {
		t.Run(raw, func(t *testing.T) {
			want := prettyJSONOrRaw(raw) // frozen pre-fix test-only reference
			nlines := strings.Count(want, "\n") + 1
			b := testDiffBudget(1<<20, 20000)
			b.limits.MaxLines = nlines
			for _, maxBytes := range []int{len(want), len(want) + 1} {
				got, err := b.pretty(raw, maxBytes)
				if err != nil || got != want {
					t.Fatalf("exact encoding/limit differs: got%q err%v want%q", got, err, want)
				}
			}
			if len(want) > 0 {
				if _, err := b.pretty(raw, len(want)-1); !errors.Is(err, errDiffTooLarge) {
					t.Fatalf("byte limit+1 accepted: %v", err)
				}
			}
			b.limits.MaxLines = nlines - 1
			if _, err := b.pretty(raw, 1<<20); !errors.Is(err, errDiffTooLarge) {
				t.Fatalf("line limit+1 accepted: %v", err)
			}
		})
	}
	// Nested indentation can exceed the budget even with very small raw JSON.
	raw := strings.Repeat("[", 60) + "0" + strings.Repeat("]", 60)
	if _, _, err := testDiffBudget(1024, 2000).prettyPair(raw, `0`); !errors.Is(err, errDiffTooLarge) {
		t.Fatalf("expanded indentation accepted: %v", err)
	}
	// Raw combined input is also budgeted, even when formatting removes spaces.
	if _, _, err := testDiffBudget(8, 100).prettyPair("   1   ", " 2 "); !errors.Is(err, errDiffTooLarge) {
		t.Fatalf("raw pair overage accepted: %v", err)
	}
	a, c := `{"a":1}`, `{"b":2}`
	size := len(prettyJSONOrRaw(a)) + len(prettyJSONOrRaw(c))
	if _, _, err := testDiffBudget(size, 100).prettyPair(a, c); err != nil {
		t.Fatal(err)
	}
	if _, _, err := testDiffBudget(size-1, 100).prettyPair(a, c); !errors.Is(err, errDiffTooLarge) {
		t.Fatalf("pretty combined overage accepted: %v", err)
	}
}

func TestDiffBudgetRenderPreservesHunksAndRefusesBeforeCompute(t *testing.T) {
	for _, pair := range [][2]string{{"", ""}, {"", "x"}, {"x", "x"}, {"a\nb\n", "a\nc\n"}, {"a\na\nb", "b\na\na"}, {"\xff\n", "\xfe\n"}} {
		b := testDiffBudget(1024, 100)
		want, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(pair[0]), B: difflib.SplitLines(pair[1]), FromFile: "old", ToFile: "new", Context: 3})
		if err != nil {
			t.Fatal(err)
		}
		got, err := b.render(pair[0], pair[1], "old", "new")
		if err != nil || got != want {
			t.Fatalf("hunks changed: %q %v want%q", got, err, want)
		}
		legacy, adds, dels, legacyErr := buildUnifiedDiff([]byte(pair[0]), []byte(pair[1]))
		canonical, canonicalErr := b.render(pair[0], pair[1], "canonical (current)", "canonical (post-merge)")
		gotAdds, gotDels := countDiffEdits(canonical)
		if legacyErr != nil || canonicalErr != nil || canonical != legacy || gotAdds != adds || gotDels != dels {
			t.Fatalf("canonical diff/stats changed: %v %v", legacyErr, canonicalErr)
		}

	}
	if got := difflib.SplitLines("x\n"); !reflect.DeepEqual(got, []string{"x\n", "\n"}) {
		t.Fatalf("unexpected library boundary: %#v", got)
	}
	b := testDiffBudget(4, 2)
	calls := 0
	b.compute = func(w io.Writer, d difflib.UnifiedDiff) error { calls++; return difflib.WriteUnifiedDiff(w, d) }
	for _, pair := range [][2]string{{"abc", "de"}, {"a\nb\n", ""}} {
		if out, err := b.render(pair[0], pair[1], "a", "b"); out != "" || !errors.Is(err, errDiffTooLarge) {
			t.Fatalf("overbudget accepted: %q %v", out, err)
		}
	}
	if calls != 0 {
		t.Fatal("rejected input entered diff computation")
	}
	if _, err := b.render("a\n", "b\n", "a", "b"); err != nil || calls != 1 {
		t.Fatalf("exact byte and line boundary: calls%d err%v", calls, err)
	}
}

func TestDiffBudgetRetainsFinalFlushErrors(t *testing.T) {
	b := testDiffBudget(16, 8)
	limit := 4*b.limits.MaxInputBytes + (128 << 10)
	d := difflib.UnifiedDiff{A: difflib.SplitLines(""), B: difflib.SplitLines("x"), FromFile: "a", ToFile: "b", Context: 3}
	short, err := difflib.GetUnifiedDiffString(d)
	if err != nil {
		t.Fatal(err)
	}
	// Only a test header is large. Endpoints use fixed short file labels.
	d.FromFile = strings.Repeat("h", limit-len(short)+2)
	full, err := difflib.GetUnifiedDiffString(d)
	if err != nil || len(full) != limit+1 {
		t.Fatalf("bad flush fixture: len%d err%v", len(full), err)
	}
	writer := &diffOutputWriter{limit: limit}
	if err := difflib.WriteUnifiedDiff(writer, d); err != nil {
		t.Fatalf("expected discarded deferred flush error, got %v", err)
	}
	if !errors.Is(writer.err, errDiffTooLarge) {
		t.Fatal("missing sticky error")
	}
	if out, err := b.render("", "x", d.FromFile, d.ToFile); out != "" || !errors.Is(err, errDiffTooLarge) {
		t.Fatalf("partial output accepted: len%d err%v", len(out), err)
	}
}

func TestDiffBudgetAdmissionAndOverflowBoundaries(t *testing.T) {
	b := testDiffBudget(8, 8)
	release, ok := b.acquire()
	if !ok {
		t.Fatal("initial admission refused")
	}
	if extra, ok := b.acquire(); ok {
		extra()
		release()
		t.Fatal("capacity exceeded")
	}
	release()
	release, ok = b.acquire()
	if !ok {
		t.Fatal("slot leaked")
	}
	release()
	if err := b.checkLengths(3, 5); err != nil {
		t.Fatal(err)
	}
	for _, ns := range [][]int{{3, 6}, {-1}, {int(^uint(0) >> 1), 1}} {
		if err := b.checkLengths(ns...); !errors.Is(err, errDiffTooLarge) {
			t.Fatalf("invalid lengths accepted: %v %v", ns, err)
		}
	}
}

func FuzzDiffBudgetPrettyMatchesEncoder(f *testing.F) {
	for _, raw := range []string{`{"a":[1,2]}`, `"<&>"`, `{}`, "\xff", "invalid\nJSON"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		want := prettyJSONOrRaw(raw)
		if len(want) > 1<<20 {
			t.Skip()
		}
		b := testDiffBudget(max(len(raw), len(want))+1, strings.Count(want, "\n")+1)
		got, err := b.pretty(raw, b.limits.MaxInputBytes)
		if err != nil || got != want {
			t.Fatalf("encoding mismatch got%q err%v want%q", got, err, want)
		}
	})
}
