package artifacts

import (
	"context"
	"database/sql"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestQuotaLevelBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		used, limit int64
		near        int
		want        string
	}{
		{"empty", 0, 1000, 80, QuotaLevelNone},
		{"just below near", 799, 1000, 80, QuotaLevelNone},
		{"exactly near", 800, 1000, 80, QuotaLevelNear},
		{"just below critical", 949, 1000, 80, QuotaLevelNear},
		{"exactly critical", 950, 1000, 80, QuotaLevelCritical},
		{"one byte left", 999, 1000, 80, QuotaLevelCritical},
		{"exactly full", 1000, 1000, 80, QuotaLevelFull},
		{"over full", 1001, 1000, 80, QuotaLevelFull},
		{"near above critical collapses", 960, 1000, 97, QuotaLevelNone},
		{"near above critical is critical", 970, 1000, 97, QuotaLevelCritical},
		{"fractional near boundary", 2, 3, 66, QuotaLevelNear},
		{"fractional below near boundary", 1, 3, 34, QuotaLevelNone},
		{"huge values do not overflow", math.MaxInt64 / 10 * 9, math.MaxInt64, 80, QuotaLevelNear},
		{"huge values critical", math.MaxInt64 - 1, math.MaxInt64, 80, QuotaLevelCritical},
		{"withheld limit", 0, 0, 80, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := quotaLevel(tc.used, tc.limit, tc.near); got != tc.want {
				t.Fatalf("quotaLevel(%d,%d,%d)=%q want %q", tc.used, tc.limit, tc.near, got, tc.want)
			}
		})
	}
}

func TestVersionStoreQuotaReportsLevelConsistently(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{MaxHistoryBytes: 1 << 20})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "first"), a); err != nil {
		t.Fatal(err)
	}
	q, err := s.Quota(t.Context(), k, a)
	if err != nil || q.Level != QuotaLevelNone || q.Warning || q.UsedBytes <= 0 || q.LimitBytes != 1<<20 {
		t.Fatalf("roomy quota %+v %v", q, err)
	}
	// Recompute the exact limit that puts this history at near, critical and full.
	for _, tc := range []struct {
		limit   int64
		level   string
		warning bool
	}{
		{q.UsedBytes * 100 / 80, QuotaLevelNear, true},
		{q.UsedBytes * 100 / 95, QuotaLevelCritical, true},
		{q.UsedBytes, QuotaLevelFull, true},
	} {
		tight := NewVersionStore(s.db, VersionOptions{Enabled: true, MaxHistoryBytes: tc.limit})
		state, err := tight.Read(t.Context(), k, a)
		if err != nil || state.Quota.Level != tc.level || state.Quota.Warning != tc.warning {
			t.Fatalf("limit %d: %+v %v", tc.limit, state.Quota, err)
		}
		view, err := tight.View(t.Context(), k, func(ctx2 context.Context, tx *sql.Tx) (string, VersionSelection, error) {
			actor, e := a(ctx2, tx)
			return actor, VersionSelection{IncludeHistory: true}, e
		})
		if err != nil || view.State.Quota != state.Quota {
			t.Fatalf("view quota differs from read: %+v vs %+v %v", view.State.Quota, state.Quota, err)
		}
	}
}

func TestPlanRetentionReportsWithoutDeleting(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return tsformat.Format(now.Add(-d)) }
	day := 24 * time.Hour
	versions := []VersionInfo{
		{Version: 12, CreatedAt: at(time.Hour)},                                            // latest
		{Version: 11, CreatedAt: at(13 * day)},                                             // inside window
		{Version: 10, CreatedAt: at(20*day + time.Hour)},                                   // newest of its day
		{Version: 9, CreatedAt: at(20*day + 2*time.Hour)},                                  // same day, older -> thin
		{Version: 8, CreatedAt: at(20*day + 3*time.Hour)},                                  // same day, shared pin
		{Version: 7, CreatedAt: at(20*day + 4*time.Hour), Message: "Before client review"}, // named
		{Version: 6, CreatedAt: at(20*day + 5*time.Hour)},                                  // same day -> thin
		{Version: 5, CreatedAt: at(30*day + time.Hour), RestoredFrom: 2},                   // a restore
		{Version: 4, CreatedAt: at(30*day + 2*time.Hour)},                                  // same day -> thin
		{Version: 3, CreatedAt: "not a time"},                                              // unknown age -> keep
		{Version: 2, CreatedAt: at(40*day + time.Hour)},                                    // restore source
		{Version: 1, CreatedAt: at(40*day + 2*time.Hour)},                                  // same day -> thin
	}
	before := append([]VersionInfo(nil), versions...)
	got := PlanRetention(versions, 12, 8, now, RetentionPolicy{RecentDays: 14})
	if !got.DryRun || got.RecentDays != 14 || got.Policy != "recent-14d-then-daily" {
		t.Fatalf("policy %+v", got)
	}
	if want := []int64{1, 4, 6, 9}; !reflect.DeepEqual(got.WouldThin, want) {
		t.Fatalf("would thin %v want %v", got.WouldThin, want)
	}
	if got.Retained != 8 || got.Total != 12 {
		t.Fatalf("counts %+v", got)
	}
	if !reflect.DeepEqual(versions, before) {
		t.Fatal("PlanRetention mutated its input")
	}
	if empty := PlanRetention(nil, 0, 0, now, RetentionPolicy{}); empty.WouldThin == nil || len(empty.WouldThin) != 0 || empty.RecentDays != DefaultRetentionPolicy.RecentDays {
		t.Fatalf("empty plan %+v", empty)
	}
}

func TestVersionStoreViewReportsRetentionPreviewOnlyWithHistory(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	for base := int64(0); base < 3; base++ {
		if _, err := s.Apply(t.Context(), k, versionRequest(base, "v"+string(rune('a'+base))), a); err != nil {
			t.Fatal(err)
		}
	}
	old := tsformat.Format(time.Now().Add(-60 * 24 * time.Hour))
	if _, err := db.Exec(`UPDATE artifact_versions SET created_at=? WHERE workspace_id='ws' AND page_id='page' AND version IN (1,2)`, old); err != nil {
		t.Fatal(err)
	}
	owner := func(ctx2 context.Context, tx *sql.Tx) (string, VersionSelection, error) {
		actor, e := a(ctx2, tx)
		return actor, VersionSelection{IncludeHistory: true}, e
	}
	view, err := s.View(t.Context(), k, owner)
	if err != nil || view.Retention == nil {
		t.Fatalf("owner view %+v %v", view.Retention, err)
	}
	if want := []int64{1}; !reflect.DeepEqual(view.Retention.WouldThin, want) {
		t.Fatalf("would thin %v want %v", view.Retention.WouldThin, want)
	}
	// Dry run: every version is still readable afterwards.
	versions, err := s.Versions(t.Context(), k, a)
	if err != nil || len(versions) != 3 {
		t.Fatalf("history changed: %d %v", len(versions), err)
	}
	if err = s.Share(t.Context(), k, 3, 3, a); err != nil {
		t.Fatal(err)
	}
	reader, err := s.View(t.Context(), k, func(ctx2 context.Context, tx *sql.Tx) (string, VersionSelection, error) {
		actor, e := a(ctx2, tx)
		return actor, VersionSelection{SharedOnly: true}, e
	})
	if err != nil || reader.Retention != nil {
		t.Fatalf("shared reader must not see retention preview: %+v %v", reader.Retention, err)
	}
}
