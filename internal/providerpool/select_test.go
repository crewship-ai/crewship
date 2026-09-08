package providerpool

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSelect(t *testing.T) {
	now := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	account := func(id string) Candidate {
		return Candidate{ID: id, OwnerID: "owner", Provider: "OPENAI", Mode: "subscription", Active: true}
	}
	a, b, c := account("a"), account("b"), account("c")
	a.LastSelected = 3
	b.LastSelected = 1
	c.Priority = 1
	policy := Policy{Provider: "OPENAI", Mode: "subscription"}
	for _, tt := range []struct {
		name    string
		mutate  func([]Candidate)
		policy  Policy
		want    string
		wantErr error
	}{
		{name: "least recently selected in highest priority", policy: policy, want: "b"},
		{name: "deterministic tie", policy: policy, mutate: func(cs []Candidate) { cs[0].LastSelected = 1 }, want: "a"},
		{name: "priority wins over rotation", policy: policy, mutate: func(cs []Candidate) { cs[0].Priority = -1 }, want: "a"},
		{name: "cooldown excluded", policy: policy, mutate: func(cs []Candidate) { cs[1].CooldownUntil = now.Add(time.Minute) }, want: "a"},
		{name: "cooldown ends at deadline", policy: policy, mutate: func(cs []Candidate) { cs[1].CooldownUntil = now }, want: "b"},
		{name: "expired excluded", policy: policy, mutate: func(cs []Candidate) { cs[1].ExpiresAt = now }, want: "a"},
		{name: "refresh must happen before selection", policy: policy, mutate: func(cs []Candidate) { cs[1].ExpiresAt = now.Add(-time.Second) }, want: "a"},
		{name: "revoked excluded", policy: policy, mutate: func(cs []Candidate) { cs[1].Active = false }, want: "a"},
		{name: "manual intervention excluded", policy: policy, mutate: func(cs []Candidate) { cs[1].Blocked = true }, want: "a"},
		{name: "fallback priority", policy: policy, mutate: func(cs []Candidate) { cs[0].Active = false; cs[1].Active = false }, want: "c"},
		{name: "no cooled account fallback", policy: policy, mutate: func(cs []Candidate) {
			for i := range cs {
				cs[i].CooldownUntil = now.Add(time.Hour)
			}
		}, wantErr: ErrUnavailable},
		{name: "cross owner requires consent", policy: policy, mutate: func(cs []Candidate) { cs[2].OwnerID = "other" }, wantErr: ErrCrossOwner},
		{name: "inactive owner does not bypass consent", policy: policy, mutate: func(cs []Candidate) { cs[2].OwnerID = "other"; cs[2].Active = false }, wantErr: ErrCrossOwner},
		{name: "cross owner opt in", policy: Policy{Provider: "OPENAI", Mode: "subscription", AllowCrossOwner: true}, mutate: func(cs []Candidate) { cs[2].OwnerID = "other" }, want: "b"},
		{name: "missing owner fails closed", policy: policy, mutate: func(cs []Candidate) { cs[1].OwnerID = "" }, wantErr: ErrInvalid},
		{name: "missing owner not cured by consent", policy: Policy{Provider: "OPENAI", Mode: "subscription", AllowCrossOwner: true}, mutate: func(cs []Candidate) { cs[1].OwnerID = "" }, wantErr: ErrInvalid},
		{name: "wrong provider", policy: policy, mutate: func(cs []Candidate) { cs[1].Provider = "ANTHROPIC" }, wantErr: ErrInvalid},
		{name: "mixed billing modes", policy: policy, mutate: func(cs []Candidate) { cs[1].Mode = "api_key" }, wantErr: ErrInvalid},
		{name: "duplicate account", policy: policy, mutate: func(cs []Candidate) { cs[1].ID = "a" }, wantErr: ErrInvalid},
		{name: "missing identity", policy: policy, mutate: func(cs []Candidate) { cs[1].ID = "" }, wantErr: ErrInvalid},
		{name: "unknown mode", policy: Policy{Provider: "OPENAI", Mode: "invented"}, wantErr: ErrInvalid},
		{name: "unknown provider", policy: Policy{Provider: "invented", Mode: "subscription"}, wantErr: ErrInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cs := []Candidate{a, b, c}
			if tt.mutate != nil {
				tt.mutate(cs)
			}
			before := append([]Candidate(nil), cs...)
			got, err := Select(tt.policy, cs, now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got.ID != tt.want {
				t.Fatalf("selected = %q, want %q", got.ID, tt.want)
			}
			if !reflect.DeepEqual(cs, before) {
				t.Fatal("selection mutated input snapshot")
			}
		})
	}
	if _, err := Select(policy, nil, now); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty pool: %v", err)
	}
	if _, err := Select(policy, []Candidate{a}, time.Time{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing clock: %v", err)
	}
}

func TestSelectRoundRobinAndPriorityRecovery(t *testing.T) {
	now := time.Now()
	policy := Policy{Provider: "OPENAI", Mode: "api_key"}
	cs := []Candidate{
		{ID: "b", OwnerID: "u", Provider: "OPENAI", Mode: "api_key", Active: true},
		{ID: "a", OwnerID: "u", Provider: "OPENAI", Mode: "api_key", Active: true},
		{ID: "fallback", OwnerID: "u", Provider: "OPENAI", Mode: "api_key", Active: true, Priority: 1},
	}
	for seq, want := range []string{"a", "b", "a", "b"} {
		got, err := Select(policy, cs, now)
		if err != nil || got.ID != want {
			t.Fatalf("selection %d = %q, %v; want %s", seq, got.ID, err, want)
		}
		for i := range cs {
			if cs[i].ID == got.ID {
				cs[i].LastSelected = int64(seq + 1)
			}
		}
	}
	for i := 0; i < 2; i++ {
		cs[i].CooldownUntil = now.Add(time.Minute)
	}
	got, err := Select(policy, cs, now)
	if err != nil || got.ID != "fallback" {
		t.Fatalf("fallback = %q, %v", got.ID, err)
	}
	got, err = Select(policy, cs, now.Add(time.Minute))
	if err != nil || got.ID != "a" {
		t.Fatalf("recovered = %q, %v", got.ID, err)
	}
}
