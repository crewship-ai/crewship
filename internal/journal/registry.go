package journal

// entryTypeSet is the O(1) membership index behind Registered, built once at
// package init from AllEntryTypes (internal/journal/registry_generated.go).
//
// Hand-written rather than generated: unlike AllEntryTypes — whose CONTENT
// is exactly "what cmd/gen-journal-registry found", so regenerating it is
// the only way it can be right — this logic never changes when an entry
// type is added, removed or renamed. Emitting it through the same
// text/template-by-WriteString machinery as the slice bought nothing but an
// extra place a typo in generated code could hide.
var entryTypeSet = func() map[EntryType]struct{} {
	m := make(map[EntryType]struct{}, len(AllEntryTypes))
	for _, t := range AllEntryTypes {
		m[t] = struct{}{}
	}
	return m
}()

// Registered reports whether t is one of the closed set of journal entry
// types AllEntryTypes carries — every value cmd/gen-journal-registry found
// declared or used as a journal.EntryType across internal/ and cmd/, not
// only internal/journal/types.go. A caller taking an event type from
// outside the process — an automation, a webhook filter, a CLI flag —
// should reject anything this returns false for: an unregistered type looks
// shaped like a real one and will never fire, silently.
func Registered(t EntryType) bool {
	_, ok := entryTypeSet[t]
	return ok
}
