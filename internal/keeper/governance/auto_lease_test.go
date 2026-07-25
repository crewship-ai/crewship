package governance

import (
	"context"
	"testing"
)

// TestAutoLeaseDefaultsOff pins the opt-in contract: a workspace that has never
// been configured must resolve to AutoLeaseSeconds 0, i.e. a Keeper ALLOW leaves
// the grant standing exactly as it did before #1373's second increment. If this
// ever defaults to a positive value, every existing deployment silently starts
// expiring credentials mid-run.
func TestAutoLeaseDefaultsOff(t *testing.T) {
	db := openTestDB(t)

	if got := Resolve(context.Background(), db, nil, "ws1").AutoLeaseSeconds; got != 0 {
		t.Fatalf("unconfigured AutoLeaseSeconds = %d, want 0 (opt-in)", got)
	}
}

// TestAutoLeaseRoundTrips asserts the knob survives Upsert→Get.
func TestAutoLeaseRoundTrips(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := Upsert(ctx, db, "ws1", Settings{DenyNotifyMinRisk: 7, AutoLeaseSeconds: 900}, "u1"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	s, found, err := Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after Upsert")
	}
	if s.AutoLeaseSeconds != 900 {
		t.Fatalf("AutoLeaseSeconds = %d, want 900", s.AutoLeaseSeconds)
	}

	// Turning it back off must persist as off, not fall through to a stale value.
	if err := Upsert(ctx, db, "ws1", Settings{DenyNotifyMinRisk: 7, AutoLeaseSeconds: 0}, "u1"); err != nil {
		t.Fatalf("Upsert off: %v", err)
	}
	s, _, err = Get(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("Get after off: %v", err)
	}
	if s.AutoLeaseSeconds != 0 {
		t.Fatalf("AutoLeaseSeconds after disable = %d, want 0", s.AutoLeaseSeconds)
	}
}

// TestAutoLeaseClamped covers the three clamp branches. A too-short lease is the
// dangerous one: it would lapse inside the gatekeeper's own LLM round-trip, so
// every ALLOW would be followed by a refusal at the injection point.
func TestAutoLeaseClamped(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"negative becomes off", -1, 0},
		{"below the floor is raised", 5, MinAutoLeaseSeconds},
		{"at the floor is kept", MinAutoLeaseSeconds, MinAutoLeaseSeconds},
		{"above the cap is clamped", MaxAutoLeaseSeconds + 1, MaxAutoLeaseSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			if err := Upsert(ctx, db, "ws1", Settings{DenyNotifyMinRisk: 7, AutoLeaseSeconds: tc.in}, "u1"); err != nil {
				t.Fatalf("Upsert: %v", err)
			}
			s, _, err := Get(ctx, db, "ws1")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if s.AutoLeaseSeconds != tc.want {
				t.Fatalf("AutoLeaseSeconds = %d, want %d", s.AutoLeaseSeconds, tc.want)
			}
		})
	}
}
