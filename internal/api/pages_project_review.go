package api

// The authorized review snapshot (docs/prd/pages.md §11, editor contract
// `lib/pages/editor-contract.ts` → `ReviewSnapshotWire`).
//
// Publishing an application replaces executable code AND the Page declaration
// that binds it to crews, producers and routines. The reviewer therefore has
// to be shown three different bases at once — the candidate source revision,
// the live publication, and the live panel definition — and they are three
// separate series that never line up.
//
// The endpoint exists so the values the human reviewed can be handed back to
// the publish call as a fence (`expected_definition_digest`,
// `expected_routine_digests`). Refetching in the browser cannot provide that
// property: it re-reads the world at publish time, which is exactly the window
// the fence closes.
//
// Two truths this response refuses to blur:
//
//   - A missing retained baseline is NOT "no changes". `source_available:false`
//     with a concrete reason and a `baseline_unavailable` blocker, never an
//     empty diff that reads as agreement.
//   - A routine with no recorded digest is `unknown`, never `unchanged`. The
//     publication's `checks_json` is provenance; a gap in it is a gap, not a
//     guarantee.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

type reviewActorWire struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Resolved NOW against the current directory, never a name snapshot taken
	// at the time of the change: `actor_json` stores ids only. Omitted rather
	// than invented when the id no longer resolves.
	Label string `json:"label,omitempty"`
}

type reviewBuildWire struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	ArtifactDigest string `json:"artifact_digest"`
	Error          string `json:"error,omitempty"`
}

type reviewCandidateWire struct {
	Revision     int64            `json:"revision"`
	GitCommit    string           `json:"git_commit"`
	SourceDigest string           `json:"source_digest"`
	CreatedAt    string           `json:"created_at"`
	Actor        reviewActorWire  `json:"actor"`
	Build        *reviewBuildWire `json:"build"`

	// Definition is the Page document this candidate would make live — the
	// draft's, or in `?publication=N` mode the archived one that version
	// recorded — authorized for this viewer by exactly the rule that
	// authorizes Baseline.Definition, in the same request.
	//
	// It is here for the same reason the baseline's is. The candidate
	// document was being read from `GET .../project`, a different endpoint on
	// a different cache entry, so the two halves of the comparison a human
	// reads could come from two moments just as the baseline and its digest
	// could. Both documents now come from this handler, so the two halves of
	// one comparison can no longer be a fresh read against a lagging cache.
	// That is per-read consistency, not a point-in-time view of the database:
	// this handler issues about ten separate autocommit statements and a
	// concurrent write can land between any two of them. What makes a
	// comparison safe to consent to is that the publish fence re-reads and
	// compares inside the writing transaction, so a stale snapshot is refused
	// rather than published.
	//
	// And withholding has to happen on BOTH sides or it lies: a panel taken
	// out of the baseline alone, with the candidate's copy of it left whole,
	// renders as a panel this candidate ADDS — on the one screen that must
	// not make a false claim about what is being published. So the panels
	// removed are the UNION of what each document had to keep back, and
	// Baseline.ExcludedPanels counts them.
	//
	// `null` under the same condition as the baseline's: the stored bytes
	// cannot be read as a Page document. When there is no candidate at all
	// this whole object is null and the question does not arise.
	Definition json.RawMessage `json:"definition"`
}

type reviewBaselineWire struct {
	PublicationVersion int64   `json:"publication_version"`
	Published          bool    `json:"published"`
	DefinitionDigest   string  `json:"definition_digest"`
	SourceRevision     *int64  `json:"source_revision"`
	GitCommit          *string `json:"git_commit"`
	SourceAvailable    bool    `json:"source_available"`
	// A sentence, not a code: it is shown where the diff would have been.
	SourceUnavailableReason *string `json:"source_unavailable_reason"`

	// Definition is the live Page document ITSELF, read from the same
	// `pages.spec_json` row, in the same request, as DefinitionDigest above.
	//
	// It is here because the screen and the fence were reading two different
	// endpoints. The change list a human reads was derived from the Page
	// detail query and the fence value was taken from this one: two caches,
	// two moments. Another author saving the live definition moved this
	// endpoint's digest while the detail query still held the old document,
	// so a person could read a comparison against definition A and send a
	// request attesting to digest B — and the server accepts it, because B is
	// genuinely current. No server-side check can catch that; it is a
	// client-side correspondence failure, and the only cure is to stop having
	// two reads. One authorized read produces both values, so they cannot
	// describe different documents. Both can still be stale by the time a
	// publication arrives; `expected_definition_digest` is what closes that,
	// inside the publishing transaction.
	//
	// It is NOT what the fence compares, and it is not necessarily the whole
	// document: panels this viewer may not read are removed from it (and
	// from Candidate.Definition, by the same rule in the same request).
	// DefinitionDigest stays the digest of the full stored bytes, withheld
	// panels included, because the fence must keep comparing what the server
	// stores.
	//
	// `null` only when the stored document cannot be read as a Page document
	// (see pageAuthorizedDefinition); a client that gets null has no basis for
	// a comparison and must not offer consent on one.
	Definition json.RawMessage `json:"definition"`

	// ExcludedPanels is how many panels were withheld from THE COMPARISON —
	// from Definition, from Candidate.Definition, or from both — so the
	// screen can say the comparison it is showing is partial.
	//
	// One number, and a panel withheld from both sides counts once, because
	// it is one panel the reader cannot see. It lives on the baseline rather
	// than on each document because the reader is looking at one comparison,
	// not two lists. Zero when neither document rendered.
	ExcludedPanels int `json:"excluded_panels"`

	// WithheldChanged is true when at least one of those withheld panels
	// actually DIFFERS between the live definition and the candidate.
	//
	// The count alone was a warning, not a policy. Removing a panel from both
	// documents stops the leak and stops the phantom addition, but the
	// consent underneath still said the whole change had been reviewed — and
	// that is reachable, not theoretical: publishing needs
	// mayAdministerGrants (manage role OR isPageOwner) and seeing a panel
	// needs canSeePanel (manage role OR membership of that panel's crew), so
	// a Page owner who is not a workspace admin may publish a Page while
	// reading only their own crews' panels.
	//
	// The server holds both FULL documents and the client holds neither, so
	// the server answers the only question that settles it. False means the
	// reader's comparison covers everything that moves, which is a true
	// statement they can scope their consent to; publishing stays available.
	// True adds the `withheld_change` blocker here and is refused with a 403
	// on every publish path — see pageWithheldChangeMessage.
	//
	// Nothing about WHAT changed is on the wire. The flag, and the count
	// already beside it, are the whole disclosure.
	WithheldChanged bool `json:"withheld_changed"`

	// DefinitionDiverged says the live Page definition and the definition the
	// LIVE PUBLICATION shipped with have drifted apart — somebody changed
	// panels outside the application flow.
	//
	// A statement, not a refusal, and it used to be a blocker. That was worse
	// than useless: the comparison on screen is derived from the CURRENT live
	// definition and the candidate, so it is complete and correct whatever
	// the publication once shipped; the thing that drifted is a third
	// document nobody is being asked to approve. Refusing on it blocked the
	// one action that ends the divergence — publishing the candidate is what
	// puts the two back in agreement — and the screen's advice, "refresh the
	// review", could never help, because a refetch says exactly the same
	// thing. The only way out of the editor was `crewship page rollback`.
	//
	// It is NOT the `definition_moved` blocker, which is reserved for the
	// publish fence tripping: a base moved between the render and the click,
	// the snapshot is stale, and refreshing IS the cure. Standing divergence
	// and a stale snapshot are different facts with opposite remedies, and
	// giving them one name is how one got the other's behaviour.
	//
	// False whenever there is no live publication to have diverged from.
	DefinitionDiverged bool `json:"definition_diverged"`
}

type reviewRoutineWire struct {
	Routine         string  `json:"routine"`
	PublishedDigest *string `json:"published_digest"`
	CurrentDigest   *string `json:"current_digest"`
	State           string  `json:"state"`
	// InCandidate marks the rows that belong in the publish fence: exactly
	// the routines the candidate document declares, which is exactly the key
	// set pageRoutineDigestsIn will rebuild and compare against.
	//
	// The list itself stays a UNION, because a routine the live publication
	// called and the candidate drops is worth seeing. Fencing on one is a
	// different matter: the server's map would not have that key, movedRoutines
	// would report it, and the publication would be refused naming a routine
	// nobody moved — permanently, since every refetch says the same thing.
	// The flag is on the wire so no client has to re-derive the server's own
	// choice of candidate document to avoid that.
	InCandidate bool `json:"in_candidate"`
}

type reviewBlockerWire struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type reviewCapabilitiesWire struct {
	MayEditSpec bool `json:"may_edit_spec"`
	MayPublish  bool `json:"may_publish"`
}

type reviewSnapshotWire struct {
	IssuedAt           string                 `json:"issued_at"`
	Candidate          *reviewCandidateWire   `json:"candidate"`
	Baseline           reviewBaselineWire     `json:"baseline"`
	Routines           []reviewRoutineWire    `json:"routines"`
	Capabilities       reviewCapabilitiesWire `json:"capabilities"`
	Blockers           []reviewBlockerWire    `json:"blockers"`
	InitialPublication bool                   `json:"initial_publication"`
}

// Blocker codes, spelled once. The UI shows the message; tests assert the code.
const (
	reviewBlockerNoCandidate       = "no_candidate"
	reviewBlockerMatchesLive       = "candidate_matches_live"
	reviewBlockerBuildMissing      = "build_missing"
	reviewBlockerBuildFailed       = "build_failed"
	reviewBlockerBuildStale        = "build_stale"
	reviewBlockerBaselineMissing   = "baseline_unavailable"
	reviewBlockerRoutineUnresolved = "routine_unresolved"
	// reviewBlockerDefinitionMoved is reserved for the publish fence
	// tripping. ReviewProject does not emit it: a standing divergence between
	// the live definition and the published one is Baseline.DefinitionDiverged
	// and is advisory. The two facts have opposite remedies.
	reviewBlockerDefinitionMoved    = "definition_moved"
	reviewBlockerWithheldChange     = "withheld_change"
	reviewBlockerNotPermitted       = "not_permitted"
	reviewBlockerStorageUnavailable = "storage_unavailable"
)

// pageDefinitionDigest is the value the publish fence compares: sha256 over the
// exact stored `pages.spec_json` bytes. Hashing the bytes rather than a
// re-marshalled document means a formatting-only rewrite still counts as a
// move, which is the conservative direction for a fence.
func pageDefinitionDigest(spec string) string {
	sum := sha256.Sum256([]byte(spec))
	return hex.EncodeToString(sum[:])
}

// pageDeclaredPanel is the only part of a panel this file reads: who owns it
// (the ACL) and what it is called (how the two documents line up).
type pageDeclaredPanel struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
}

// pageDefinitionAuthorizer applies §7.1 rule 2 — "a panel's visibility IS its
// owning crew's visibility" — to a panel as DECLARED, rather than to a
// `page_panels` row.
//
// It has to be the declaration, because the candidate document is not a row in
// anything: it is the document that would become the live Page. A candidate
// that adds a panel owned by a crew the viewer is in has no row to consult,
// and a candidate that re-points a panel at another crew is asking for a
// verdict the OLD row would answer backwards. So the owner reference in the
// document being rendered decides, for both documents, and one rule cannot
// drift from itself.
//
// The verdict itself is still canSeePanel's — the same function the Page
// detail route seals with, carve-outs included — fed the crew the declaration
// names. For the live document the two inputs are the same fact: reconcile
// writes `page_panels.owner_crew_id` from exactly this field.
type pageDefinitionAuthorizer struct {
	h      *PageHandler
	viewer *pageViewer
	crews  map[string]string // crew slug → crew id, live crews only
}

// visible answers for one `owner: "crew/<slug>"` reference. Anything it cannot
// resolve to a live crew is denied, so an owner the document does not state,
// states in another shape, or points at a deleted crew withholds the panel.
func (a pageDefinitionAuthorizer) visible(ownerRef string) bool {
	slug := strings.TrimPrefix(ownerRef, "crew/")
	if slug == ownerRef {
		slug = ""
	}
	return a.h.canSeePanel(a.viewer, &panelRecord{OwnerCrewID: a.crews[slug]})
}

// reviewDefinitionAuthorizer resolves the caller's standing and the crew
// directory once per request, for both documents on the snapshot.
//
// `mayEditSpec` — which is all projectPage proved — does not imply the caller
// may see every panel: panel visibility is its owning crew's (§7.1 rule 2),
// and mayEditSpec admits a `write` grantee with no crew standing at all.
//
// Be exact about what this withholding is, because it is easy to read as more
// than it is. It keeps the REVIEW SURFACE from rendering and comparing what
// this reader cannot read — so the change list is not built out of another
// crew's panels, so `excluded_panels` can say the comparison is partial, and
// so `withheld_changed` can refuse an attestation nobody could honestly make.
//
// It is NOT a confidentiality boundary, and nothing here should be cited as
// one. `GET .../project` (pages_project.go, loadProject) and
// `GET .../project/history/{revision}` (pages_project_history.go,
// projectRevision) serve the COMPLETE unfiltered Page document to the same
// callers behind the identical projectPage → mayEditSpec gate, and the review
// screen calls the first of them on every render. A caller withheld from here
// is one request away from the whole document.
//
// Those two are not filtered here on purpose: PutProject writes the whole
// document back, so serving a filtered draft to a caller who then saves it
// would DELETE the withheld panels — the failure that closed the document
// editor on sealed pages in the first place. Fixing them needs a round-trip
// story, and that is a separate change with its own issue.
func (h *PageHandler) reviewDefinitionAuthorizer(ctx context.Context, ws string) (pageDefinitionAuthorizer, error) {
	auth := pageDefinitionAuthorizer{h: h, viewer: h.reviewViewer(ctx, ws), crews: map[string]string{}}
	rows, err := h.db.QueryContext(ctx, `SELECT slug,id FROM crews WHERE workspace_id=? AND deleted_at IS NULL`, ws)
	if err != nil {
		return auth, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug, id string
		if err := rows.Scan(&slug, &id); err != nil {
			return auth, err
		}
		auth.crews[slug] = id
	}
	return auth, rows.Err()
}

// reviewViewer is the standing canSeePanel is applied with on this route, and
// it never returns nil: a nil viewer means "unscoped, serve everything" to
// canSeePanel, which is the one answer this endpoint must never give.
//
// Two principals reach the project routes (projectPage). A human is their
// workspace role plus their crews, read the same way every other page route
// reads it. A project agent has no user row and no workspace role; its whole
// standing is the owner crew the internal token was minted against, so that is
// the only crew it may see panels of.
func (h *PageHandler) reviewViewer(ctx context.Context, ws string) *pageViewer {
	if actor := projectAgentFrom(ctx); actor != nil {
		crews := map[string]bool{}
		if actor.OwnerCrewID != "" {
			crews[actor.OwnerCrewID] = true
		}
		return &pageViewer{Crews: crews}
	}
	user := UserFromContext(ctx)
	if user == nil {
		return &pageViewer{Crews: map[string]bool{}}
	}
	viewer, err := h.loadViewer(ctx, ws, user.ID)
	if err != nil || viewer == nil {
		// A standing that could not be read is no standing. Reading the
		// document is not worth guessing an ACL for.
		return &pageViewer{Crews: map[string]bool{}}
	}
	return viewer
}

// pageDefinitionPanels reaches the panel array inside a stored Page document
// without modelling the document.
//
// Raw JSON rather than a round trip through pages.Document on purpose: the
// review screen's job is to show EVERY change, including fields no Go struct
// models, and a typed round trip would silently drop them from one side of
// the comparison and then report the other side as having added them.
//
// `ok` is false when the bytes are not a document this can walk safely, which
// is the only thing that makes a definition null on the wire. A document with
// no `spec` or no `panels` key is perfectly walkable and simply has no panels.
func pageDefinitionPanels(spec string) (doc, specObject map[string]json.RawMessage, panels []json.RawMessage, ok bool) {
	if err := json.Unmarshal([]byte(spec), &doc); err != nil {
		return nil, nil, nil, false
	}
	rawSpec, has := doc["spec"]
	if !has {
		return doc, nil, nil, true
	}
	if err := json.Unmarshal(rawSpec, &specObject); err != nil {
		return nil, nil, nil, false
	}
	rawPanels, has := specObject["panels"]
	if !has {
		return doc, specObject, nil, true
	}
	if err := json.Unmarshal(rawPanels, &panels); err != nil {
		return nil, nil, nil, false
	}
	return doc, specObject, panels, true
}

// pageWithheldPanelIDs names the panels in one document this viewer may not
// read, and counts separately the withheld panels that carry no id.
//
// An unnamed panel cannot be matched against the other document, so it is
// withheld on principle and counted where it is found rather than pretending
// to be the same panel as some unnamed panel over there.
func pageWithheldPanelIDs(spec string, visible func(ownerRef string) bool) (ids []string, unnamed int) {
	_, _, panels, ok := pageDefinitionPanels(spec)
	if !ok {
		return nil, 0
	}
	for _, panel := range panels {
		var declared pageDeclaredPanel
		// A panel that will not parse is withheld rather than served: there
		// is no owner to check it against, so there is no evidence this
		// viewer may read it.
		if err := json.Unmarshal(panel, &declared); err == nil && declared.ID != "" && visible(declared.Owner) {
			continue
		}
		if declared.ID == "" {
			unnamed++
			continue
		}
		ids = append(ids, declared.ID)
	}
	return ids, unnamed
}

// pageAuthorizedDocument is pageAuthorizedDefinition parsed back into the
// typed document the routine helpers walk. False means the bytes could not be
// authorized or re-read, which is a server fault rather than a verdict.
func pageAuthorizedDocument(spec string, visible func(ownerRef string) bool, drop map[string]bool) (*pages.Document, bool) {
	authorized := pageAuthorizedDefinition(spec, visible, drop)
	if authorized == nil {
		return nil, false
	}
	var doc pages.Document
	if err := json.Unmarshal(authorized, &doc); err != nil {
		return nil, false
	}
	return &doc, true
}

// pageWithheldPanels is what one viewer cannot read across the two documents
// a publication compares, and whether any of it moves.
//
// It exists so the review endpoint and the publish handler run ONE
// computation. The review's `withheld_changed` is the server promising that
// the comparison on screen covers everything; the publish refusal is the
// server keeping that promise. Two implementations of the same sentence would
// eventually disagree, and the direction they would disagree in is a
// publication that the review said was complete.
type pageWithheldPanels struct {
	// IDs is the union across both documents: a panel withheld from either
	// side is out of the comparison on both, so no phantom addition or
	// removal can appear.
	IDs map[string]bool
	// Unnamed counts withheld panels carrying no id, which cannot be matched
	// across the documents and therefore cannot be shown not to have moved.
	Unnamed int
	// Changed is the attestation question: does anything the reader cannot
	// see differ between what is live and what would replace it.
	Changed bool
}

// Count is what `excluded_panels` reports: panels withheld from the
// comparison, a panel withheld from both documents counted once.
func (p pageWithheldPanels) Count() int { return len(p.IDs) + p.Unnamed }

// pageWithheldPanelsBetween decides both halves for one viewer.
//
// candidateSpec is "" when there is no candidate. Nothing is being published
// then, so nobody is attesting to anything and Changed is false; the withheld
// set is still computed from the live document, because the screen still
// renders it.
func pageWithheldPanelsBetween(liveSpec, candidateSpec string, visible func(ownerRef string) bool) pageWithheldPanels {
	out := pageWithheldPanels{IDs: map[string]bool{}}
	liveIDs, liveUnnamed := pageWithheldPanelIDs(liveSpec, visible)
	candidateIDs, candidateUnnamed := pageWithheldPanelIDs(candidateSpec, visible)
	for _, ids := range [][]string{liveIDs, candidateIDs} {
		for _, id := range ids {
			out.IDs[id] = true
		}
	}
	out.Unnamed = liveUnnamed + candidateUnnamed
	out.Changed = pageWithheldChanged(liveSpec, candidateSpec, out)
	return out
}

// pageWithheldChanged compares the withheld panels of the two FULL documents.
//
// Every uncertainty resolves to "changed". A document that will not parse, or
// a withheld panel with no id to match on, is not evidence that nothing moved
// — and the whole point of this answer is that somebody is about to swear
// something on it.
//
// A panel being RE-POINTED between a crew the viewer may see and one they may
// not needs no special case: whichever side withholds it puts its id in the
// union, and its `owner` then differs between the two documents. That is the
// change which would otherwise vanish completely, since the panel is removed
// from both rendered documents.
func pageWithheldChanged(liveSpec, candidateSpec string, withheld pageWithheldPanels) bool {
	if candidateSpec == "" {
		return false
	}
	if withheld.Unnamed > 0 {
		return true
	}
	if len(withheld.IDs) == 0 {
		return false
	}
	live, liveOK := pageDefinitionPanelsByID(liveSpec)
	candidate, candidateOK := pageDefinitionPanelsByID(candidateSpec)
	if !liveOK || !candidateOK {
		return true
	}
	for id := range withheld.IDs {
		before, inLive := live[id]
		after, inCandidate := candidate[id]
		if inLive != inCandidate {
			return true
		}
		if inLive && !reflect.DeepEqual(before, after) {
			return true
		}
	}
	return false
}

// pageDefinitionPanelsByID parses each panel to a VALUE rather than keeping
// its bytes: key order and whitespace carry no meaning in JSON, and a
// document that was merely re-marshalled must not read as a change. An
// unparseable panel makes the whole document unknown, which fails closed.
func pageDefinitionPanelsByID(spec string) (map[string]any, bool) {
	_, _, panels, ok := pageDefinitionPanels(spec)
	if !ok {
		return nil, false
	}
	out := make(map[string]any, len(panels))
	for _, panel := range panels {
		var declared pageDeclaredPanel
		if err := json.Unmarshal(panel, &declared); err != nil {
			return nil, false
		}
		// Unnamed panels are counted separately and never reach this map.
		if declared.ID == "" {
			continue
		}
		var value any
		if err := json.Unmarshal(panel, &value); err != nil {
			return nil, false
		}
		out[declared.ID] = value
	}
	return out, true
}

// pageWithheldChangeMessage is the one sentence both refusals use: the review
// blocker and the publish 403.
//
// Neutral by construction. It says that such a part exists and how many
// panels are withheld — a number `excluded_panels` already carries — and
// nothing about what changed, which crew owns it, or what it is called. It
// also says who CAN publish, because the alternative is a person retrying a
// request that will never succeed for them.
func pageWithheldChangeMessage(withheld int) string {
	panels := "panels are"
	if withheld == 1 {
		panels = "panel is"
	}
	return fmt.Sprintf("Part of this Page is withheld from you and this publication changes it (%d %s withheld from this comparison). "+
		"Publishing attests that the whole change was reviewed, and that attestation cannot be made by a publisher who cannot read all of it. "+
		"A workspace administrator, or a member of the crew that owns the withheld part, can publish it.", withheld, panels)
}

// pageAuthorizedDefinition removes from a document every panel this viewer may
// not read, and every panel whose id is in `drop`.
//
// `drop` is what makes the withholding SYMMETRIC, and symmetry is the whole
// point. The baseline and the candidate are compared panel by panel and
// matched by id, so a panel removed from one side and left whole on the other
// renders as an addition or a removal — a false claim about a panel that is
// simply not this reader's to see. The caller passes the union of what both
// documents withheld, and the two sides then agree on which panels are not
// part of the comparison at all. `baseline.excluded_panels` is how many that
// is, so the screen can say the comparison is partial rather than complete.
//
// Removed rather than sealed to a stub: a stub still has to be filtered out by
// whoever compares the documents, and a client re-deriving a decision the
// server already made is the shape of bug this whole change is undoing.
//
// Returns nil — `definition: null` on the wire — when the bytes are not a Page
// document. Every branch that cannot identify a panel removes it, so the
// failure direction is always "withhold", never "disclose".
func pageAuthorizedDefinition(spec string, visible func(ownerRef string) bool, drop map[string]bool) json.RawMessage {
	doc, specObject, panels, ok := pageDefinitionPanels(spec)
	if !ok {
		return nil
	}
	kept := make([]json.RawMessage, 0, len(panels))
	for _, panel := range panels {
		var declared pageDeclaredPanel
		if err := json.Unmarshal(panel, &declared); err != nil || declared.ID == "" {
			continue
		}
		if !visible(declared.Owner) || drop[declared.ID] {
			continue
		}
		kept = append(kept, panel)
	}
	// Nothing withheld: hand back the stored bytes untouched, so the document
	// the reviewer reads is literally the one the digest was taken over.
	if len(kept) == len(panels) {
		return json.RawMessage(spec)
	}
	repacked, err := json.Marshal(kept)
	if err != nil {
		return nil
	}
	specObject["panels"] = repacked
	rewrittenSpec, err := json.Marshal(specObject)
	if err != nil {
		return nil
	}
	doc["spec"] = rewrittenSpec
	out, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return out
}

// ReviewProject serves the snapshot a human reviews and then publishes against.
//
// It is deliberately a 200 even when publishing is impossible: a reviewer who
// cannot publish still needs to read the candidate, and a Page whose artifact
// store is unconfigured still has a definition worth showing. Every refusal is
// a blocker in the body, never a status code that erases the rest.
//
// Two candidates, one shape. Without `?publication=N` the candidate is the
// current draft. With it, the candidate is retained publication N — what
// `rollback_version: N` would publish. That mode exists because the fence
// recomputes `expected_routine_digests` from the ARCHIVED spec of the version
// being restored, and nothing else on the wire tells a caller that key set:
// the draft's routines are a different document's routines, and deriving the
// set from the archived spec client-side would still leave the caller without
// each routine's current digest. Only the server can answer both halves, so
// it answers them here rather than making the browser guess and get a 400.
//
// `baseline` is the same in both modes: publishing a retained version replaces
// the same live definition, so it is fenced against the same digest.
func (h *PageHandler) ReviewProject(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.projectPage(w, r)
	if !ok {
		return
	}
	// SHARED, not exclusive (pageLease → ProjectStore.Lease(..., false)). It
	// keeps this read off a compacting or checkpoint-rewriting storage
	// operation; it serialises nothing against an ordinary publication, and
	// this handler opens no transaction, so the statements below are ten
	// separate autocommit reads that a concurrent write can interleave with.
	// The snapshot is therefore per-read consistent and not a point-in-time
	// view — see Baseline.Definition for what actually makes it safe to
	// consent to.
	release, leased := h.pageLease(w, r)
	if !leased {
		return
	}
	defer release()
	ctx := r.Context()
	ws := WorkspaceIDFromContext(ctx)
	user := UserFromContext(ctx)
	// A malformed cursor is a caller error, not a blocker: there is no
	// candidate the caller could have meant.
	requested := int64(0)
	if raw := r.URL.Query().Get("publication"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			replyError(w, 400, "publication must be a positive publication version")
			return
		}
		requested = parsed
	}

	snapshot := reviewSnapshotWire{
		IssuedAt: h.evaluator().Now().UTC().Format(time.RFC3339),
		Routines: []reviewRoutineWire{},
		Blockers: []reviewBlockerWire{},
		Capabilities: reviewCapabilitiesWire{
			MayEditSpec: true, // projectPage already proved it.
			MayPublish:  user != nil && h.mayAdministerGrants(ctx, ws, user.ID, RoleFromContext(ctx), rec),
		},
	}
	blocked := func(code, message string) {
		snapshot.Blockers = append(snapshot.Blockers, reviewBlockerWire{Code: code, Message: message})
	}

	// The live Page declaration. This is the fence value, and it is read even
	// when nothing has ever been published: a Page always has a definition.
	var liveSpec string
	if err := h.db.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id=?`, rec.ID).Scan(&liveSpec); err != nil {
		replyInternalError(w, h.logger, "read live Page definition", err)
		return
	}
	snapshot.Baseline.DefinitionDigest = pageDefinitionDigest(liveSpec)
	// The same bytes, rendered for this viewer. Reading them here rather than
	// leaving the screen to fetch the document from the Page detail route is
	// the whole point: the digest above and the document below are derived
	// from one statement's result, so they always describe the same document.
	// They do not describe the same instant as anything else on this snapshot
	// — the publication rows, the draft and the routines are separate
	// statements and can move between them.
	authorizer, err := h.reviewDefinitionAuthorizer(ctx, ws)
	if err != nil {
		replyInternalError(w, h.logger, "resolve page definition authorization", err)
		return
	}

	var publications int
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_project_publications WHERE page_id=?`, rec.ID).Scan(&publications); err != nil {
		replyInternalError(w, h.logger, "count Page publications", err)
		return
	}
	snapshot.InitialPublication = publications == 0

	var publishedSpec, publishedChecks, publishedSource string
	if !snapshot.InitialPublication {
		var revision int64
		var commit string
		err := h.db.QueryRowContext(ctx, `SELECT p.version,l.published,p.source_revision,p.git_commit,p.source_digest,p.spec_json,p.checks_json FROM page_project_live l JOIN page_project_publications p ON p.page_id=l.page_id AND p.version=l.version WHERE l.page_id=?`, rec.ID).
			Scan(&snapshot.Baseline.PublicationVersion, &snapshot.Baseline.Published, &revision, &commit, &publishedSource, &publishedSpec, &publishedChecks)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, h.logger, "read live publication", err)
			return
		}
		if err == nil {
			snapshot.Baseline.SourceRevision = &revision
			snapshot.Baseline.GitCommit = &commit
			snapshot.Baseline.SourceAvailable, snapshot.Baseline.SourceUnavailableReason = h.reviewBaselineSource(ctx, ws, rec.ID, commit, publishedSpec)
			if !snapshot.Baseline.SourceAvailable {
				blocked(reviewBlockerBaselineMissing, *snapshot.Baseline.SourceUnavailableReason)
			}
			// The live declaration drifting away from the one this publication
			// shipped is a real, detectable condition (a panel rollback, an
			// import) and worth showing. It is not a reason to refuse: see
			// Baseline.DefinitionDiverged.
			//
			// It is reported as a flag rather than a sentence deliberately.
			// The sentence this used to send said "the definition the RUNNING
			// application shipped with", which is false in the one state this
			// branch also reaches after a withdrawal — the same snapshot says
			// `published: false`, so nothing is running — and left the client
			// composing a truthful replacement for that case. The true
			// statement in both states is about the LIVE PUBLICATION, not a
			// running application; whoever writes the note must say that.
			snapshot.Baseline.DefinitionDiverged = publishedSpec != liveSpec
		} else {
			// Publications exist but no live pointer: nothing is running, so
			// there is no retained baseline to compare against either.
			reason := "This Page has publication history but no live publication pointer, so there is no retained application source to compare against."
			snapshot.Baseline.SourceUnavailableReason = &reason
			blocked(reviewBlockerBaselineMissing, reason)
		}
	} else {
		reason := "This Page has never published an application, so there is no prior application source to compare against."
		snapshot.Baseline.SourceUnavailableReason = &reason
		// Deliberately NO baseline_unavailable: an initial publication is
		// allowed. Only a MISSING retained baseline for an existing
		// publication is a refusal.
	}

	// The candidate, the document its routines are fenced on, and whatever
	// else is worth showing beside them.
	var candidateSpec, contextSpec, comparisonChecks string
	if requested > 0 {
		spec, checks, ok := h.reviewRetainedCandidate(w, r, rec, requested, &snapshot, blocked)
		if !ok {
			return
		}
		candidateSpec, comparisonChecks = spec, checks
	} else {
		spec, ok := h.reviewDraftCandidate(w, r, rec, publishedSource, publishedSpec, &snapshot, blocked)
		if !ok {
			return
		}
		candidateSpec, comparisonChecks = spec, publishedChecks
	}
	// With no candidate there is nothing to fence, so nothing is in_candidate;
	// the live definition still supplies rows so drift stays visible.
	if snapshot.Candidate == nil {
		candidateSpec, contextSpec = "", liveSpec
	}
	// Both documents the reviewer compares, out of one handler and one rule.
	// Each document is internally consistent with the statement that produced
	// it; they are not a joint snapshot of the database, and nothing here
	// needs them to be. The withheld set is the UNION of what each side had to
	// keep back, applied to both, so a panel this reader may not see is
	// absent from the comparison rather than showing up as the candidate
	// adding or removing it.
	withheld := pageWithheldPanelsBetween(liveSpec, candidateSpec, authorizer.visible)
	snapshot.Baseline.Definition = pageAuthorizedDefinition(liveSpec, authorizer.visible, withheld.IDs)
	snapshot.Baseline.ExcludedPanels = withheld.Count()
	snapshot.Baseline.WithheldChanged = withheld.Changed
	if snapshot.Candidate != nil {
		snapshot.Candidate.Definition = pageAuthorizedDefinition(candidateSpec, authorizer.visible, withheld.IDs)
	}
	// A count is a warning; this is the policy. The same computation refuses
	// the publication with a 403, so the button being absent here and the
	// request being refused there can never disagree.
	if withheld.Changed {
		blocked(reviewBlockerWithheldChange, pageWithheldChangeMessage(withheld.Count()))
	}
	// The routine rows come from the SAME authorized documents as the two
	// definitions above, not from the stored ones. Filtering the panels and
	// then listing the routines they call would be withholding and disclosing
	// in one response.
	routines, err := h.reviewRoutines(ctx, ws,
		string(pageAuthorizedDefinition(candidateSpec, authorizer.visible, withheld.IDs)),
		string(pageAuthorizedDefinition(contextSpec, authorizer.visible, withheld.IDs)),
		comparisonChecks,
		string(pageAuthorizedDefinition(publishedSpec, authorizer.visible, withheld.IDs)))
	if err != nil {
		replyInternalError(w, h.logger, "read routine definitions", err)
		return
	}
	snapshot.Routines = routines
	// A candidate calling a routine that no longer resolves is not merely
	// `unknown`: publishing it is refused with a 422 further down, so the
	// review has to say so too. Reporting only the state told the reviewer
	// that publication was available when it was not.
	for _, row := range routines {
		if row.InCandidate && row.CurrentDigest == nil {
			blocked(reviewBlockerRoutineUnresolved, fmt.Sprintf("This candidate has an action calling routine %q, which no longer exists in this workspace; publishing is refused until the routine is restored or the action removed.", row.Routine))
		}
	}

	if h.pageArtifacts == nil || h.pageRuntimeOrigin == "" {
		blocked(reviewBlockerStorageUnavailable, "Page build storage and a separate runtime origin must be configured before an application can be published.")
	}
	if !snapshot.Capabilities.MayPublish {
		blocked(reviewBlockerNotPermitted, "Publishing an application requires Page ownership or workspace administration.")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, snapshot)
}

// reviewDraftCandidate fills in the current draft as the candidate and returns
// the draft's definition — the document the fence is computed from — or "" when
// there is no candidate to publish.
func (h *PageHandler) reviewDraftCandidate(w http.ResponseWriter, r *http.Request, rec *pageRecord, publishedSource, publishedSpec string, snapshot *reviewSnapshotWire, blocked func(string, string)) (string, bool) {
	ctx := r.Context()
	ws := WorkspaceIDFromContext(ctx)
	var draftRevision int64
	var draftDigest, draftCommit, draftSpec string
	err := h.db.QueryRowContext(ctx, `SELECT revision,source_digest,git_commit,spec_json FROM page_project_drafts WHERE page_id=?`, rec.ID).Scan(&draftRevision, &draftDigest, &draftCommit, &draftSpec)
	hasDraft := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read project draft", err)
		return "", false
	}
	switch {
	case !hasDraft:
		blocked(reviewBlockerNoCandidate, "This Page has no application draft to review.")
		return "", true
	// Identical means BOTH bases agree: the same source bytes and the same
	// declaration. Two different sources can declare the same panels, and the
	// same source can be saved with a changed declaration.
	//
	// And it only means "already live" while the publication is actually
	// running. After a withdrawal nothing is serving, and republishing the
	// same source is the recovery path, not a no-op — calling it
	// candidate_matches_live would null the candidate and leave the caller
	// with an empty fence for a candidate that does declare routines.
	case snapshot.Baseline.Published && publishedSource != "" && draftDigest == publishedSource && draftSpec == publishedSpec:
		blocked(reviewBlockerMatchesLive, "The current draft is identical to the live publication; there is nothing new to review.")
		return "", true
	}
	candidate := &reviewCandidateWire{Revision: draftRevision, GitCommit: draftCommit, SourceDigest: draftDigest}
	var actorUser, actorJSON string
	if err := h.db.QueryRowContext(ctx, `SELECT created_at,COALESCE(actor_user_id,''),actor_json FROM page_project_revisions WHERE page_id=? AND revision=?`, rec.ID, draftRevision).Scan(&candidate.CreatedAt, &actorUser, &actorJSON); err != nil && !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "read candidate revision", err)
		return "", false
	}
	candidate.Actor = h.reviewActor(ctx, ws, actorUser, actorJSON)
	build, ok := h.reviewBuild(ctx, rec.ID, draftRevision)
	if !ok {
		replyInternalError(w, h.logger, "read candidate build", errors.New("build lookup failed"))
		return "", false
	}
	candidate.Build = build
	snapshot.Candidate = candidate
	switch {
	case build != nil && build.State == "ready":
		// Publishable as far as the build goes.
	case build != nil && (build.State == "failed" || build.State == "interrupted"):
		blocked(reviewBlockerBuildFailed, "The build of this draft revision did not complete; build the revision again before publishing.")
	default:
		stale, err := h.reviewHasReadyBuildElsewhere(ctx, rec.ID, draftRevision)
		if err != nil {
			replyInternalError(w, h.logger, "read ready builds", err)
			return "", false
		}
		if stale {
			blocked(reviewBlockerBuildStale, "The only ready build is of an earlier source revision; build the current draft before publishing.")
		} else {
			blocked(reviewBlockerBuildMissing, "The current draft revision has no ready build.")
		}
	}
	return draftSpec, true
}

// reviewRetainedCandidate fills in retained publication `version` as the
// candidate — the one `rollback_version: version` would publish — and returns
// that publication's ARCHIVED spec and its recorded checks_json.
//
// The archived spec is what the publish fence recomputes routine digests from,
// so `routines[]` derived from it is exactly the key set the server will
// compare against. That equality is the whole reason this mode exists.
//
// No new blocker codes were needed. A version that is not retained is
// `no_candidate` — there is nothing to review — and a version that is already
// running is `candidate_matches_live`, which is the same statement the draft
// mode makes about a draft that is already live. `build_stale` has no meaning
// here: a retained publication names one build and there is no newer revision
// of it to be stale against.
//
// Unlike the draft mode, the candidate stays populated when it matches live.
// The caller named this version; nulling it would leave the screen unable to
// say WHICH version it is refusing, and the routines it needs are still real.
func (h *PageHandler) reviewRetainedCandidate(w http.ResponseWriter, r *http.Request, rec *pageRecord, version int64, snapshot *reviewSnapshotWire, blocked func(string, string)) (string, string, bool) {
	ctx := r.Context()
	ws := WorkspaceIDFromContext(ctx)
	candidate := &reviewCandidateWire{}
	var buildID, spec, checks, actorUser string
	err := h.db.QueryRowContext(ctx, `SELECT build_id,source_revision,source_digest,git_commit,spec_json,checks_json,COALESCE(actor_user_id,''),created_at FROM page_project_publications WHERE page_id=? AND version=?`, rec.ID, version).
		Scan(&buildID, &candidate.Revision, &candidate.SourceDigest, &candidate.GitCommit, &spec, &checks, &actorUser, &candidate.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		blocked(reviewBlockerNoCandidate, fmt.Sprintf("Publication %d is not retained for this Page; read the publication history for the versions that are.", version))
		return "", "", true
	}
	if err != nil {
		replyInternalError(w, h.logger, "read retained publication", err)
		return "", "", false
	}
	// The actor a publication recorded is its PUBLISHER, not the author of the
	// source revision, and the column is cleared when that person is erased.
	// An erased publisher is `unknown`; borrowing the revision's author would
	// answer a different question than the field asks.
	candidate.Actor = h.reviewActor(ctx, ws, actorUser, "")
	var build reviewBuildWire
	build.ID = buildID
	err = h.db.QueryRowContext(ctx, `SELECT state,COALESCE(artifact_digest,''),error FROM page_project_builds WHERE id=?`, buildID).Scan(&build.State, &build.ArtifactDigest, &build.Error)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Publishing verifies the recorded build, so a compacted one refuses.
		blocked(reviewBlockerBuildMissing, fmt.Sprintf("The build publication %d recorded is no longer retained, so this version cannot be republished.", version))
	case err != nil:
		replyInternalError(w, h.logger, "read retained publication build", err)
		return "", "", false
	default:
		candidate.Build = &build
		if build.State != "ready" {
			blocked(reviewBlockerBuildFailed, fmt.Sprintf("The build publication %d recorded is %s, not ready, so this version cannot be republished.", version, build.State))
		}
	}
	if snapshot.Baseline.Published && snapshot.Baseline.PublicationVersion == version {
		blocked(reviewBlockerMatchesLive, fmt.Sprintf("Publication %d is already the live application; there is nothing to restore.", version))
	}
	snapshot.Candidate = candidate
	return spec, checks, true
}

// reviewBaselineSource answers whether the live publication's retained source
// still reads back, through the same ReadCheckpoint path publish verifies with.
//
// Anything short of "reads back and matches the recorded declaration" is
// unavailable. A checkpoint that returns different bytes is worse than a
// missing one, and both produce a diff basis that would be a lie.
func (h *PageHandler) reviewBaselineSource(ctx context.Context, ws, page, commit, publishedSpec string) (bool, *string) {
	reason := func(text string) (bool, *string) { return false, &text }
	if commit == "" {
		return reason("The live publication predates Git checkpoints, so its source cannot be read back for comparison.")
	}
	if h.projectStore == nil {
		return reason("Page project storage is not configured on this server, so the published source cannot be read back for comparison.")
	}
	source, archived, err := h.projectStore.ReadCheckpoint(ctx, ws, page, commit)
	if err != nil {
		return reason("The retained source for the live publication (checkpoint " + commit + ") could not be read back: " + err.Error())
	}
	if _, err := source.Digest(); err != nil {
		return reason("The retained source for the live publication (checkpoint " + commit + ") failed integrity verification: " + err.Error())
	}
	if archived != publishedSpec {
		return reason("The retained source for the live publication (checkpoint " + commit + ") carries a different Page definition than the publication recorded.")
	}
	return true, nil
}

// reviewBuild returns the most recent build of exactly this revision, or nil.
func (h *PageHandler) reviewBuild(ctx context.Context, page string, revision int64) (*reviewBuildWire, bool) {
	var b reviewBuildWire
	err := h.db.QueryRowContext(ctx, `SELECT id,state,COALESCE(artifact_digest,''),error FROM page_project_builds WHERE page_id=? AND source_revision=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, page, revision).Scan(&b.ID, &b.State, &b.ArtifactDigest, &b.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	return &b, true
}

func (h *PageHandler) reviewHasReadyBuildElsewhere(ctx context.Context, page string, revision int64) (bool, error) {
	var count int
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_project_builds WHERE page_id=? AND state='ready' AND source_revision<>?`, page, revision).Scan(&count)
	return count > 0, err
}

// reviewRoutines compares the routine definitions this publication recorded
// with the ones that would run now.
//
// `unknown` covers both gaps: the publication recorded no digest for this
// routine, and the routine no longer resolves in this workspace. Neither is
// evidence of agreement, and rendering either as `unchanged` is the defect
// this state exists to prevent.
//
// Every spec argument is the AUTHORIZED document, never the stored one, and
// that is load-bearing rather than tidy. A routine is named by a panel's
// `call` action and a panel's visibility is its owning crew's, so a row for a
// routine only a withheld panel declares would hand this reader the
// association — that a panel they may not read calls it — plus a change
// oracle on another crew's routine, in the same body that just withheld the
// panel. The rule is: a routine is served only when a panel that SURVIVED
// withholding declares it.
//
// candidateSpec is the authorized document being published — empty when there
// is no candidate — and its routines are the ones flagged `in_candidate`,
// which is also exactly the key set the publish fence rebuilds from the same
// authorized document. The two must stay identical: offering a key the fence
// does not want is a 409 naming a routine nobody moved, and wanting a key the
// review never offered is a publication that can never be made.
// contextSpec adds rows worth showing that are not part of the fence; it is
// the live definition when there is no candidate at all, so a Page without a
// draft still shows routine drift.
// publishedSpec is the authorized document the recorded `checks_json` came
// from. It is what filters that map: `checks_json` is a bare name→digest map
// with no panel attribution, so without it a routine that only a withheld
// panel ever declared would come back through the published side.
func (h *PageHandler) reviewRoutines(ctx context.Context, ws, candidateSpec, contextSpec, checksJSON, publishedSpec string) ([]reviewRoutineWire, error) {
	candidate := pageDeclaredRoutines(candidateSpec)
	context := pageDeclaredRoutines(contextSpec)
	declared := pageDeclaredRoutines(publishedSpec)
	published := map[string]string{}
	if checksJSON != "" {
		var checks pageRoutineChecks
		if err := json.Unmarshal([]byte(checksJSON), &checks); err == nil {
			for name, digest := range checks.Definitions {
				// Provenance for a routine no authorized panel declares is
				// provenance about somebody else's panel.
				if declared[name] || candidate[name] || context[name] {
					published[name] = digest
				}
			}
		}
	}
	names := make([]string, 0, len(published)+len(candidate)+len(context))
	seen := map[string]bool{}
	for _, set := range []map[string]bool{published2set(published), candidate, context} {
		for name := range set {
			if !seen[name] {
				seen[name], names = true, append(names, name)
			}
		}
	}
	sort.Strings(names)
	out := make([]reviewRoutineWire, 0, len(names))
	for _, name := range names {
		row := reviewRoutineWire{Routine: name, State: "unknown", InCandidate: candidate[name]}
		if digest, ok := published[name]; ok && digest != "" {
			value := digest
			row.PublishedDigest = &value
		}
		var definition string
		err := h.db.QueryRowContext(ctx, `SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL`, ws, name).Scan(&definition)
		if err == nil {
			value := pageRoutineDigest(definition)
			row.CurrentDigest = &value
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if row.PublishedDigest != nil && row.CurrentDigest != nil {
			if *row.PublishedDigest == *row.CurrentDigest {
				row.State = "unchanged"
			} else {
				row.State = "changed"
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// pageDeclaredRoutines is the set of routines a Page definition's `call`
// actions name. An unparseable document declares nothing rather than failing
// the whole review: the reviewer still needs the rest of the snapshot.
func pageDeclaredRoutines(specJSON string) map[string]bool {
	out := map[string]bool{}
	if specJSON == "" {
		return out
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(specJSON), &doc); err != nil {
		return out
	}
	for _, panel := range doc.Spec.Panels {
		for _, action := range panel.Actions {
			if action.Kind == pages.ActionCall && action.Routine != "" {
				out[action.Routine] = true
			}
		}
	}
	return out
}

func published2set(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for name := range m {
		out[name] = true
	}
	return out
}

// reviewActor turns the stored id snapshot into a kind and an id, and resolves
// a label only when a real current row answers for it.
func (h *PageHandler) reviewActor(ctx context.Context, ws, actorUser, actorJSON string) reviewActorWire {
	actor := reviewActorKind(actorUser, actorJSON)
	if actor.ID == "" {
		return actor
	}
	lookup := func(query string, args ...any) string {
		var label string
		if err := h.db.QueryRowContext(ctx, query, args...).Scan(&label); err != nil {
			return ""
		}
		return label
	}
	switch actor.Kind {
	case "user":
		actor.Label = lookup(`SELECT COALESCE(email,'') FROM users WHERE id=?`, actor.ID)
	case "agent":
		actor.Label = lookup(`SELECT COALESCE(slug,'') FROM agents WHERE id=? AND workspace_id=?`, actor.ID, ws)
	case "crew":
		actor.Label = lookup(`SELECT COALESCE(slug,'') FROM crews WHERE id=? AND workspace_id=?`, actor.ID, ws)
	}
	return actor
}

// reviewActorKind is the classification alone, with no directory read.
//
// A list endpoint wants the kind for every row and the label for none of them;
// resolving one is a query per row (51 on a page of history) whose result is
// then discarded. Splitting it keeps the single definition of what the stored
// shapes mean while letting a caller pay only for what it renders.
func reviewActorKind(actorUser, actorJSON string) reviewActorWire {
	var stored struct {
		UserID      string `json:"user_id"`
		WorkspaceID string `json:"workspace_id"`
		CrewID      string `json:"crew_id"`
		AgentID     string `json:"agent_id"`
		OwnerCrewID string `json:"owner_crew_id"`
	}
	if actorJSON != "" {
		_ = json.Unmarshal([]byte(actorJSON), &stored)
	}
	switch {
	case stored.UserID != "" || actorUser != "":
		id := stored.UserID
		if id == "" {
			id = actorUser
		}
		return reviewActorWire{Kind: "user", ID: id}
	case stored.AgentID != "":
		return reviewActorWire{Kind: "agent", ID: stored.AgentID}
	case stored.CrewID != "":
		return reviewActorWire{Kind: "crew", ID: stored.CrewID}
	}
	return reviewActorWire{Kind: "unknown", ID: ""}
}
