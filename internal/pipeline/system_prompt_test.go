package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestBuildSystemPromptBlock_EmptyWorkspace(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	out, err := BuildSystemPromptBlock(context.Background(), store, "ws_test", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty block for empty workspace, got %q", out)
	}
}

func TestBuildSystemPromptBlock_OnePipeline(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	if _, err := store.Save(context.Background(), validSaveInput("email-fetch")); err != nil {
		t.Fatalf("save: %v", err)
	}

	out, err := BuildSystemPromptBlock(context.Background(), store, "ws_test", map[string]string{
		"crew_a": "Marketing",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(out, "[AVAILABLE ROUTINES]") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "[END AVAILABLE ROUTINES]") {
		t.Error("missing footer")
	}
	if !strings.Contains(out, "slug: email-fetch") {
		t.Errorf("missing slug entry: %q", out)
	}
	// Should resolve crew_a → "Marketing" via the supplied map.
	if !strings.Contains(out, "authored by: Marketing") {
		t.Errorf("crew name not resolved: %q", out)
	}
	// Newly-saved pipeline has invocation_count=0.
	if !strings.Contains(out, "not yet invoked") {
		t.Errorf("expected 'not yet invoked' for new pipeline: %q", out)
	}
}

func TestBuildSystemPromptBlock_FallsBackToCrewIDWhenNameMissing(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	if _, err := store.Save(context.Background(), validSaveInput("p1")); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := BuildSystemPromptBlock(context.Background(), store, "ws_test", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Without the lookup map, the raw crew_id surfaces.
	if !strings.Contains(out, "authored by: crew_a") {
		t.Errorf("expected raw crew_a fallback: %q", out)
	}
}

func TestBuildSystemPromptBlock_CharBudgetCapsEntries(t *testing.T) {
	// Regression guard for prompt bloat: every agent exec pays for this
	// block, so besides the entry cap there is a total character budget.
	// A workspace full of long-description routines must not grow the
	// block past the budget — overflow is summarised as "…and N more",
	// keeping the full list one GET away instead of in every prompt.
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	longDesc := strings.Repeat("very detailed description ", 8) // ~200 chars after oneLine
	for i := 0; i < systemPromptCap; i++ {
		in := validSaveInput(fmt.Sprintf("routine-%02d", i))
		in.Description = longDesc
		if _, err := store.Save(context.Background(), in); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	out, err := BuildSystemPromptBlock(context.Background(), store, "ws_test", nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(out) > routinesPromptCharBudget+512 { // header/footer slack
		t.Errorf("block exceeds char budget: %d bytes (budget %d)", len(out), routinesPromptCharBudget)
	}
	if !strings.Contains(out, "more routine(s) not shown") {
		t.Errorf("expected overflow summary line; got %d bytes:\n%s", len(out), out[:200])
	}
	if !strings.Contains(out, "[END AVAILABLE ROUTINES]") {
		t.Error("footer must survive truncation")
	}
}

func TestBuildSystemPromptBlock_OneLineDescriptionCollapsesNewlines(t *testing.T) {
	if got := oneLine("hello\nworld\n  multi   space"); got != "hello world multi space" {
		t.Errorf("oneLine collapse: got %q", got)
	}
	long := strings.Repeat("x", 250)
	got := oneLine(long)
	// 200 chars + multibyte UTF-8 ellipsis (3 bytes) = 203 max.
	if len(got) > 203 {
		t.Errorf("oneLine truncate: length %d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("oneLine truncate ellipsis: %q", got)
	}
}
