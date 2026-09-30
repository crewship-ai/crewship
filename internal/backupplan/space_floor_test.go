package backupplan

import (
	"context"
	"strings"
	"testing"
)

// The space floor refuses a run on a nearly full disk (the default 10 %
// refuses every run on a 95 %-full one); the refusal must be visible and
// say what to do: an Overview needs-attention item pointing at Storage, the
// space card's reason, and the run's own error.
func TestOverview_SpaceFloorRefusalIsVisibleAndActionable(t *testing.T) {
	t.Setenv(MinFreePercentEnv, "") // the default floor, whatever the shell says
	h := newHarness(t, "2026-09-30T02:00:00Z")
	ctx := context.Background()
	const total = 1000 << 30
	cases := []struct {
		name    string
		free    uint64
		env     string
		refused bool
	}{
		{"95 % full refuses at the default 10 %", 50 << 30, "", true},
		{"half empty runs", 500 << 30, "", false},
		{"the documented override lets a shared disk run", 50 << 30, "3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(MinFreePercentEnv, tc.env)
			ov, err := BuildOverview(ctx, h.db, OverviewInput{Scope: ScopeInstance, Space: func() (uint64, uint64) { return tc.free, total }}, h.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			var item *AttentionItem
			for i := range ov.NeedsAttention {
				if ov.NeedsAttention[i].ID == "space" {
					item = &ov.NeedsAttention[i]
				}
			}
			if !tc.refused {
				if item != nil || ov.Space.Refusal != nil {
					t.Fatalf("refused anyway: %+v %v", item, ov.Space.Refusal)
				}
				return
			}
			if item == nil || item.Severity != "bad" || item.Action == nil || item.Action.Kind != "storage" {
				t.Fatalf("needs-attention item = %+v", item)
			}
			if item.Title != "Backups will not start: the disk is 95% full" {
				t.Errorf("title = %q", item.Title)
			}
			for _, want := range []string{"less than 10% free does not start", MinFreePercentEnv, "Free space on this disk"} {
				if !strings.Contains(item.Detail, want) {
					t.Errorf("detail %q lacks %q", item.Detail, want)
				}
			}
			if ov.Space.Refusal == nil || *ov.Space.Refusal != item.Detail || ov.Space.MinFreePercent != 10 {
				t.Errorf("space = %+v", ov.Space)
			}
			if ov.NeedsAttention[0].Severity != "bad" {
				t.Errorf("a bad item is not first: %+v", ov.NeedsAttention)
			}
		})
	}
}

func TestSpaceFloorRefusal(t *testing.T) {
	t.Setenv(MinFreePercentEnv, "")
	if r := SpaceFloorRefusal(50, 1000, 0); !strings.Contains(r, MinFreePercentEnv+" (0-50, in percent)") {
		t.Errorf("refusal does not say how to lower the floor: %q", r)
	}
	if r := SpaceFloorRefusal(500, 1000, 100); r != "" {
		t.Errorf("room enough, refused: %q", r)
	}
	if r := SpaceFloorRefusal(500, 1000, 450); r == "" {
		t.Error("a run that would end below the floor was allowed")
	}
	if r := SpaceFloorRefusal(0, 0, 10); r != "" {
		t.Errorf("unknown disk refused: %q", r)
	}
	t.Setenv(MinFreePercentEnv, "0")
	if r := SpaceFloorRefusal(50, 1000, 10); r != "" || MinFreePercent() != 0 {
		t.Errorf("floor 0 still refuses: %q", r)
	}
}
