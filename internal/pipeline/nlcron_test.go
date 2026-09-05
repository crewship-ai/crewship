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

// TestNextOccurrences_PragueDSTSpring proves the preview endpoint's
// arithmetic against REAL tzdata across the spring-forward transition
// (B9, #2362 accept line). Europe/Prague jumps 02:00 CET -> 03:00 CEST on
// the last Sunday of March; 2026's is 2026-03-29, so a daily 02:30 cron has
// no valid local time that day at all and must be skipped entirely rather
// than firing twice or crashing — this is what a real cron daemon does,
// and it's what the underlying tz database (not our code) decides.
func TestNextOccurrences_PragueDSTSpring(t *testing.T) {
	from := time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC) // Friday, two days before the jump
	occs, err := NextOccurrences("30 2 * * *", "Europe/Prague", 5, from)
	if err != nil {
		t.Fatalf("NextOccurrences: %v", err)
	}
	if len(occs) != 5 {
		t.Fatalf("got %d occurrences, want 5", len(occs))
	}
	for _, o := range occs {
		if o.Day() == 29 && o.Month() == time.March {
			t.Fatalf("2026-03-29 02:30 Europe/Prague does not exist (spring-forward skips 02:00-02:59) — got an occurrence on it: %v", o)
		}
	}
	want := []struct {
		day    int
		hour   int
		offset string
	}{
		{27, 2, "+01:00"}, // Fri, still CET
		{28, 2, "+01:00"}, // Sat, still CET
		// 29th (Sunday) is skipped — no 02:30 exists that day
		{30, 2, "+02:00"}, // Mon, now CEST
		{31, 2, "+02:00"}, // Tue, CEST
	}
	// Only 4 calendar days are named above (the 29th is the skip); the 5th
	// occurrence lands the following day, still CEST.
	for i, w := range want {
		if occs[i].Day() != w.day || occs[i].Hour() != w.hour {
			t.Errorf("occurrence[%d] = %v, want day %d hour %d", i, occs[i], w.day, w.hour)
		}
		if got := occs[i].Format("-07:00"); got != w.offset {
			t.Errorf("occurrence[%d] offset = %s, want %s (day %d)", i, got, w.offset, w.day)
		}
	}
	if occs[4].Day() != 1 || occs[4].Month() != time.April {
		t.Errorf("occurrence[4] = %v, want 2026-04-01 (CEST)", occs[4])
	}
}

// TestNextOccurrences_PragueDSTAutumn proves the fall-back transition: on
// 2026-10-25 Europe/Prague clocks go 03:00 CEST -> 02:00 CET, so local
// 02:30 occurs TWICE that day (once at CEST, once an hour later at CET).
// A cron scheduler samples wall-clock time, so it really does see 02:30
// twice — the two occurrences below are exactly one hour apart in UTC,
// which is the only unambiguous way to state "twice" against real tzdata.
func TestNextOccurrences_PragueDSTAutumn(t *testing.T) {
	from := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) // Friday, two days before the fold
	occs, err := NextOccurrences("30 2 * * *", "Europe/Prague", 5, from)
	if err != nil {
		t.Fatalf("NextOccurrences: %v", err)
	}
	if len(occs) != 5 {
		t.Fatalf("got %d occurrences, want 5", len(occs))
	}
	// occs[0]=Fri 23rd, occs[1]=Sat 24th, occs[2]&occs[3]=Sun 25th (twice), occs[4]=Mon 26th.
	if occs[2].Day() != 25 || occs[3].Day() != 25 {
		t.Fatalf("expected 2026-10-25 to appear twice (occs[2] and occs[3]), got %v and %v", occs[2], occs[3])
	}
	if got := occs[2].Format("-07:00"); got != "+02:00" {
		t.Errorf("occs[2] (first 02:30 on the fold day) offset = %s, want +02:00 (still CEST)", got)
	}
	if got := occs[3].Format("-07:00"); got != "+01:00" {
		t.Errorf("occs[3] (second 02:30 on the fold day) offset = %s, want +01:00 (now CET)", got)
	}
	gap := occs[3].UTC().Sub(occs[2].UTC())
	if gap != time.Hour {
		t.Errorf("the two 02:30s on the fold day must be exactly 1h apart in UTC, got %v", gap)
	}
	if occs[4].Day() != 26 {
		t.Errorf("occurrence[4] = %v, want 2026-10-26", occs[4])
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
