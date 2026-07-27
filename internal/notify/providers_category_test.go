package notify

import "testing"

// TestEveryProviderHasAKnownCategory pins the grouping the Catalog view renders.
//
// The catalog is a flat, ordered list, and the UI needs it in named sections
// ("Chat", "Push", "Incident") — otherwise 11 destinations arrive as one
// undifferentiated wall, which is what the previous provider <Select> did.
// The obvious shortcut is a name->section map in TypeScript, but then adding a
// provider to this file lands it in whatever bucket the frontend's `default`
// arm happens to be, silently and possibly wrongly. Categorising here means a
// new provider cannot be added without saying where it belongs.
func TestEveryProviderHasAKnownCategory(t *testing.T) {
	known := map[ProviderCategory]bool{
		CategoryChat:     true,
		CategoryPush:     true,
		CategoryIncident: true,
	}
	for _, p := range Providers() {
		if p.Category == "" {
			t.Errorf("provider %q has no category — the catalog would not know which section to render it in", p.Name)
			continue
		}
		if !known[p.Category] {
			t.Errorf("provider %q has unknown category %q; add it to ProviderCategories() or fix the spec", p.Name, p.Category)
		}
	}
}

// TestProviderCategoriesAreCovered keeps the declared section list and the
// catalog honest in the other direction: a section nobody is in renders as an
// empty heading, which reads as "we support this and it's broken".
func TestProviderCategoriesAreCovered(t *testing.T) {
	used := map[ProviderCategory]int{}
	for _, p := range Providers() {
		used[p.Category]++
	}
	for _, c := range ProviderCategories() {
		if used[c.Key] == 0 {
			t.Errorf("category %q (%s) has no providers — it would render as an empty section", c.Key, c.Label)
		}
	}
}

// TestKnownProviderCategories spot-checks the assignments a human would
// notice being wrong. Opsgenie under "Chat" is not a typo the compiler can
// catch, and it is exactly the kind of thing that makes a catalog feel
// untrustworthy.
func TestKnownProviderCategories(t *testing.T) {
	want := map[string]ProviderCategory{
		ProviderDiscord:    CategoryChat,
		ProviderSlack:      CategoryChat,
		ProviderTelegram:   CategoryChat,
		ProviderMattermost: CategoryChat,
		ProviderMatrix:     CategoryChat,
		ProviderTeams:      CategoryChat,
		ProviderGoogleChat: CategoryChat,
		ProviderNtfy:       CategoryPush,
		ProviderGotify:     CategoryPush,
		ProviderPushover:   CategoryPush,
		ProviderOpsgenie:   CategoryIncident,
	}
	for name, wantCat := range want {
		spec, ok := ProviderByName(name)
		if !ok {
			t.Errorf("provider %q is missing from the catalog", name)
			continue
		}
		if spec.Category != wantCat {
			t.Errorf("provider %q category = %q, want %q", name, spec.Category, wantCat)
		}
	}
}
