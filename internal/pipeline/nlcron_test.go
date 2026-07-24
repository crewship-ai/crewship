package pipeline

import (
	"testing"
	"time"
)

// #1422 item 1: NL→cron with next-3-occurrences confirmation. schedules.go
// (see the "No NL→cron converter" MVP limitation in docs/guides/routines.mdx)
// left this designed-but-unshipped; these are the failing tests written
// before ParseNaturalCron / NextOccurrences existed.

func TestParseNaturalCron(t *testing.T) {
	tests := []struct {
		name    string
		phrase  string
		want    string
		wantErr bool
	}{
		{name: "every weekday at 9", phrase: "every weekday at 9", want: "0 9 * * 1-5"},
		{name: "every weekday at 9am", phrase: "every weekday at 9am", want: "0 9 * * 1-5"},
		{name: "every day at 9am", phrase: "every day at 9am", want: "0 9 * * *"},
		{name: "every day at 9:30", phrase: "every day at 9:30", want: "30 9 * * *"},
		{name: "every day at 14:00", phrase: "every day at 14:00", want: "0 14 * * *"},
		{name: "every monday at 14:00", phrase: "every monday at 14:00", want: "0 14 * * 1"},
		{name: "every Monday At 2pm (case/spacing)", phrase: "  Every   Monday  At 2pm ", want: "0 14 * * 1"},
		{name: "every weekend at 10am", phrase: "every weekend at 10am", want: "0 10 * * 0,6"},
		{name: "every hour", phrase: "every hour", want: "0 * * * *"},
		{name: "every 15 minutes", phrase: "every 15 minutes", want: "*/15 * * * *"},
		{name: "every 2 hours", phrase: "every 2 hours", want: "0 */2 * * *"},
		{name: "unrecognized gibberish", phrase: "whenever the mood strikes", wantErr: true},
		{name: "bad time", phrase: "every day at 25:99", wantErr: true},
		{name: "60 minutes rejected (use every hour)", phrase: "every 60 minutes", wantErr: true},
		{name: "empty", phrase: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseNaturalCron(tt.phrase)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseNaturalCron(%q) = %q, want error", tt.phrase, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNaturalCron(%q) unexpected error: %v", tt.phrase, err)
			}
			if got != tt.want {
				t.Errorf("ParseNaturalCron(%q) = %q, want %q", tt.phrase, got, tt.want)
			}
		})
	}
}

// The derived cron expression must always be independently parseable by the
// real cron parser used at schedule-save time — a converter that emits
// something schedules.go's own parser rejects would be worse than useless.
func TestParseNaturalCron_OutputIsValidCron(t *testing.T) {
	phrases := []string{
		"every weekday at 9", "every day at 9am", "every monday at 14:00",
		"every weekend at 10am", "every hour", "every 15 minutes", "every 2 hours",
	}
	for _, phrase := range phrases {
		expr, err := ParseNaturalCron(phrase)
		if err != nil {
			t.Fatalf("ParseNaturalCron(%q): %v", phrase, err)
		}
		if _, err := NextOccurrences(expr, "UTC", 1, time.Now()); err != nil {
			t.Errorf("cron expr %q derived from %q is not a valid schedule: %v", expr, phrase, err)
		}
	}
}

func TestNextOccurrences(t *testing.T) {
	// Fixed anchor: Monday 2026-07-20 00:00:00 UTC.
	from := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	occs, err := NextOccurrences("0 9 * * 1-5", "UTC", 3, from)
	if err != nil {
		t.Fatalf("NextOccurrences: %v", err)
	}
	if len(occs) != 3 {
		t.Fatalf("got %d occurrences, want 3", len(occs))
	}
	want := []time.Time{
		time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC), // Monday
		time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC), // Tuesday
		time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC), // Wednesday
	}
	for i, w := range want {
		if !occs[i].Equal(w) {
			t.Errorf("occurrence[%d] = %v, want %v", i, occs[i], w)
		}
	}
}

func TestNextOccurrences_InvalidCron(t *testing.T) {
	if _, err := NextOccurrences("not a cron", "UTC", 3, time.Now()); err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestNextOccurrences_InvalidTimezone(t *testing.T) {
	if _, err := NextOccurrences("0 9 * * *", "Not/AZone", 3, time.Now()); err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}
