package api

import (
	"reflect"
	"sort"
	"testing"
)

// TestParseCapabilities covers the wire-shape ingress: NULL-ish
// inputs degrade to nil (caller falls back to FallbackCapabilitiesForRole),
// valid JSON arrays produce the expected set, unknown capability
// strings are silently dropped (forward-compat), and malformed JSON
// is rejected without panicking.
func TestParseCapabilities(t *testing.T) {
	t.Run("empty string returns nil", func(t *testing.T) {
		if got := ParseCapabilities(""); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("literal null returns nil", func(t *testing.T) {
		if got := ParseCapabilities("null"); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
	t.Run("malformed JSON returns nil", func(t *testing.T) {
		if got := ParseCapabilities(`["chat`); got != nil {
			t.Errorf("got %v, want nil for malformed input", got)
		}
	})
	t.Run("empty array returns nil", func(t *testing.T) {
		if got := ParseCapabilities(`[]`); got != nil {
			t.Errorf("got %v, want nil for empty array", got)
		}
	})
	t.Run("single known capability", func(t *testing.T) {
		got := ParseCapabilities(`["chat"]`)
		if !reflect.DeepEqual(got, map[string]struct{}{"chat": {}}) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("multiple known capabilities", func(t *testing.T) {
		got := ParseCapabilities(`["chat","routine.create","issue.create"]`)
		want := map[string]struct{}{"chat": {}, "routine.create": {}, "issue.create": {}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("unknown capability silently dropped", func(t *testing.T) {
		// Forward-compat: a future capability in the DB shouldn't lock
		// out the user — we drop it and keep the rest.
		got := ParseCapabilities(`["chat","future.capability","issue.create"]`)
		want := map[string]struct{}{"chat": {}, "issue.create": {}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("all unknown returns nil", func(t *testing.T) {
		// Edge case: a corrupt/future-only row produces nil so the
		// fallback path runs rather than locking the user out entirely.
		if got := ParseCapabilities(`["future.cap","also.future"]`); got != nil {
			t.Errorf("got %v, want nil when nothing recognized", got)
		}
	})
	t.Run("whitespace and empty entries skipped", func(t *testing.T) {
		got := ParseCapabilities(`["chat","  ","","issue.create"]`)
		want := map[string]struct{}{"chat": {}, "issue.create": {}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// TestSerializeCapabilities covers the wire-shape egress: nil/empty
// produces the chat-baseline (defensive — no row should land below
// the minimum needed to talk to an agent), populated sets serialize
// in stable alphabetical order so diff-based audit logs are
// meaningful across consecutive writes.
func TestSerializeCapabilities(t *testing.T) {
	t.Run("nil produces chat baseline", func(t *testing.T) {
		if got := SerializeCapabilities(nil); got != `["chat"]` {
			t.Errorf("got %s, want chat baseline", got)
		}
	})
	t.Run("empty produces chat baseline", func(t *testing.T) {
		if got := SerializeCapabilities(map[string]struct{}{}); got != `["chat"]` {
			t.Errorf("got %s", got)
		}
	})
	t.Run("stable alphabetical order", func(t *testing.T) {
		// Map iteration is non-deterministic in Go; the serializer
		// MUST produce the same string for equal inputs, otherwise
		// audit-log diff churn becomes a false signal.
		input := map[string]struct{}{
			"routine.create": {}, "chat": {}, "issue.create": {},
		}
		got := SerializeCapabilities(input)
		want := `["chat","issue.create","routine.create"]`
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
	t.Run("roundtrip stability", func(t *testing.T) {
		// Parse → serialize → parse → serialize must be idempotent.
		original := `["chat","issue.create","routine.create"]`
		got := SerializeCapabilities(ParseCapabilities(original))
		if got != original {
			t.Errorf("roundtrip drift: %s -> %s", original, got)
		}
	})
}

// TestHasCapability covers the runtime gate point. CapabilityChat is
// always granted (defensive — admin can't revoke chat without
// ejecting the user); other capabilities require an explicit grant.
func TestHasCapability(t *testing.T) {
	caps := map[string]struct{}{"chat": {}, "routine.create": {}}
	t.Run("chat always granted", func(t *testing.T) {
		// Even an empty set grants chat — a row that somehow ended up
		// with no capabilities still gets the baseline.
		if !HasCapability(nil, CapabilityChat) {
			t.Error("chat must be implied even for nil set")
		}
	})
	t.Run("granted capability returns true", func(t *testing.T) {
		if !HasCapability(caps, CapabilityRoutineCreate) {
			t.Error("routine.create granted but check returned false")
		}
	})
	t.Run("ungranted capability returns false", func(t *testing.T) {
		if HasCapability(caps, CapabilitySkillCreate) {
			t.Error("skill.create not granted but check returned true")
		}
	})
}

// TestFallbackCapabilitiesForRole verifies the role-derived bundle
// matches the v109 backfill exactly. If these drift, an upgrade-in-
// progress workspace (some rows with NULL, some backfilled) would
// see inconsistent permission decisions across handler calls.
func TestFallbackCapabilitiesForRole(t *testing.T) {
	cases := []struct {
		role string
		want []string
	}{
		{"OWNER", []string{"chat", "credential.create", "credential.rotate", "issue.create", "memory.write", "routine.create", "skill.create"}},
		{"ADMIN", []string{"chat", "credential.create", "credential.rotate", "issue.create", "memory.write", "routine.create", "skill.create"}},
		{"MANAGER", []string{"chat", "issue.create", "memory.write", "routine.create"}},
		{"MEMBER", []string{"chat"}},
		{"VIEWER", []string{"chat"}},
		{"", []string{"chat"}}, // unknown role degrades to chat baseline
	}
	for _, c := range cases {
		t.Run(c.role, func(t *testing.T) {
			got := FallbackCapabilitiesForRole(c.role)
			gotList := make([]string, 0, len(got))
			for k := range got {
				gotList = append(gotList, k)
			}
			sort.Strings(gotList)
			if !reflect.DeepEqual(gotList, c.want) {
				t.Errorf("role %q: got %v, want %v", c.role, gotList, c.want)
			}
		})
	}
}

// TestBundleCapabilities verifies preset bundle contents — admin CLI
// `crewship member preset` and the dashboard quick-pick depend on
// these being stable.
func TestBundleCapabilities(t *testing.T) {
	cases := []struct {
		bundle CapabilityBundle
		want   []string
	}{
		{BundleChat, []string{"chat"}},
		{BundlePower, []string{"chat", "routine.create", "issue.create", "memory.write"}},
		{BundleAdmin, []string{"chat", "routine.create", "skill.create", "credential.create", "credential.rotate", "issue.create", "memory.write"}},
		{"unknown", nil},
	}
	for _, c := range cases {
		t.Run(string(c.bundle), func(t *testing.T) {
			got := BundleCapabilities(c.bundle)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("bundle %q: got %v, want %v", c.bundle, got, c.want)
			}
		})
	}
}

// TestIsValidCapability rejects typos so admin commands fail fast
// rather than persisting unmatchable strings.
func TestIsValidCapability(t *testing.T) {
	cases := map[string]bool{
		"chat":            true,
		"routine.create":  true,
		"routine.creat":   false, // typo
		"ROUTINE.CREATE":  false, // case-sensitive on purpose
		"":                false,
		"future.thing":    false,
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := IsValidCapability(input); got != want {
				t.Errorf("IsValidCapability(%q) = %v, want %v", input, got, want)
			}
		})
	}
}

// TestAllCapabilities returns a stable-ordered list — the dashboard
// renders the checkboxes from this slice, so reorder = visual churn.
func TestAllCapabilities(t *testing.T) {
	got := AllCapabilities()
	want := []string{
		"chat",
		"credential.create",
		"credential.rotate",
		"issue.create",
		"memory.write",
		"routine.create",
		"skill.create",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
