package seeddata

// MirrorQakfDocDef is one mirrored Claude Docs document: an empty Page
// initialized as a native Docs document (schema 4) from a complete Tiptap
// body. It is named for its item so parallel mirrors cannot collide.
type MirrorQakfDocDef struct {
	Slug string
	Name string
	// File is the initialize body's document tree, under showcase/.
	File string
	// Threads lists the original's comment threads, each anchored to the
	// same quoted text in one block (UTF-16 offsets), under showcase/.
	Threads string
	// OperationID is the initialize operation's stable id: a retry after a
	// lost confirmation is answered with the original receipt.
	OperationID string
}

// MirrorQakfDoc mirrors the owner's "Template Crewship Docs" (Claude Docs,
// https://claude.ai/artifact/QakFfQpVt3oFvMVqRYFi6B). How each block was
// carried, and what a Docs page cannot hold yet, is in
// showcase/mirror/qakf/SOURCE.md. The Docs kind has no catalogue slice of its
// own, so the item is seeded by seedShowcaseMirrorQakf.
var MirrorQakfDoc = MirrorQakfDocDef{
	Slug:        "ref-template-crewship-docs",
	Name:        "Template Crewship Docs",
	File:        "mirror/qakf/document.json",
	Threads:     "mirror/qakf/threads.json",
	OperationID: "seed-ref-template-crewship-docs-1",
}
