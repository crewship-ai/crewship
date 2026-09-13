package api

// Pages folders — F1 of docs/prd/pages-collections-access-analysis-2026-09-12.md
// (§1, §4, §5/1-2, §5/8, §5/12, §6, §7; issue #2527).
//
// A folder is a named group of pages owned by ONE crew, with an icon and a
// colour from the crew's own picker. One level, a page in at most one folder;
// pages outside any folder are "unfiled". Folders carry no grants yet — that
// is F3 — so in this PR a move changes nobody's access, and every rule below
// is a rule about who may arrange, never about who may read.
//
// Who may do what (§4), the whole table, because the refusals below quote it:
//
//	create            admin; a workspace MANAGER+ who is a member of the crew
//	                  named as owner — the crew owns the folder from birth.
//	rename/icon/colour admin; the owning crew's MANAGER+.
//	delete            admin; the owning crew's MANAGER+ — and ONLY when empty,
//	                  counting pages the caller cannot see (§1/9). The
//	                  database says the same thing (ON DELETE RESTRICT).
//	add / move a page two authorities in one caller: the page's owner or an
//	                  admin INITIATES, and an admin or the target folder's
//	                  owning-crew MANAGER+ ACCEPTS. Lacking either is a 403
//	                  that names who can.
//	remove a page     the page's owner or an admin. Never the folder: taking
//	                  your page back does not wait for anyone (§1/9).
//	read a folder     whoever reaches at least one page in it, the owning
//	                  crew, or an admin — and they see only the pages they
//	                  reach and a count of those (§5/12). A folder the caller
//	                  reaches nothing in is a 404, the same posture canSeePage
//	                  takes for a page: the folder listing must not be an
//	                  oracle for what other people have filed.
//
// Two version fences (§5/8): `pages_version` on the page changes with every
// change to its folder membership, `grants_version` on the folder with every
// change to its grants (none yet). A move carries both and a stale one is a
// 409 that returns the current pair, so the client re-reads instead of
// applying a decision made against a folder that has since changed. A
// removal carries only the page's.
//
// A workspace MANAGER is "MANAGER+" here in canRole's create/update tier
// (OWNER, ADMIN, MANAGER); admin is its manage tier (OWNER, ADMIN).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// Journal entry types. File-local for the reason pages_transfer_owner.go gives:
// EntryType is a plain string type and the generated registry picks these up
// wherever they are declared.
const (
	// entryPageFolderChanged records a folder created, renamed (name, icon or
	// colour) or deleted. Payload: folder, folder_id, op, owner, actor_user_id
	// and, for a rename, the fields that changed.
	entryPageFolderChanged journal.EntryType = "page.folder_changed"
	// entryPageFolderMembershipChanged records a page moved into a folder or
	// taken out of one. Payload: page, page_id, op (moved|removed), from, to,
	// pages_version, actor_user_id.
	entryPageFolderMembershipChanged journal.EntryType = "page.folder_membership_changed"
)

// pageFolderEvent is the realtime broadcast for anything about the folder
// itself. A move or removal broadcasts the existing `page.updated` instead —
// it is the page's row that changed.
const pageFolderEvent = "page.folder.updated"

// pageFolderSlugRE is the shape internal/pages uses for a page's own slug.
var pageFolderSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

const pageFolderMaxNameRunes = 120

// ── Records and wire ───────────────────────────────────────────────────────

type pageFolderRecord struct {
	ID            string
	Slug          string
	Name          string
	Icon          string
	Color         string
	OwnerCrewID   string
	OwnerCrewSlug string
	OwnerCrewName string
	GrantsVersion int64
	CreatedAt     string
	UpdatedAt     string
}

// pageFolderRef is the folder as a page row carries it: enough to draw the
// rail entry and address the folder, nothing about the folder's other pages.
type pageFolderRef struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

// ref is nil-safe so a map lookup on an unfiled page's empty folder id renders
// straight to JSON null.
func (f *pageFolderRecord) ref() *pageFolderRef {
	if f == nil {
		return nil
	}
	return &pageFolderRef{Slug: f.Slug, Name: f.Name, Icon: f.Icon, Color: f.Color}
}

// pageFolderWire is one folder as the folder endpoints send it. PageCount is
// the number of pages in it the CALLER reaches (§5/12) — the same number of
// rows Show would return — never the folder's true size.
type pageFolderWire struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Icon          string `json:"icon"`
	Color         string `json:"color"`
	Owner         string `json:"owner"`
	OwnerCrewName string `json:"owner_crew_name"`
	PageCount     int    `json:"page_count"`
	GrantsVersion int64  `json:"grants_version"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func (f *pageFolderRecord) wire(pageCount int) pageFolderWire {
	return pageFolderWire{
		ID: f.ID, Slug: f.Slug, Name: f.Name, Icon: f.Icon, Color: f.Color,
		Owner: "crew/" + f.OwnerCrewSlug, OwnerCrewName: f.OwnerCrewName,
		PageCount: pageCount, GrantsVersion: f.GrantsVersion,
		CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
}

// pageFolderShowWire is Show's document: the folder and the pages in it the
// caller reaches, as index rows.
type pageFolderShowWire struct {
	pageFolderWire
	Pages []pageListWire `json:"pages"`
}

type pageFolderWriteRequest struct {
	Name  *string `json:"name"`
	Slug  *string `json:"slug"`
	Icon  *string `json:"icon"`
	Color *string `json:"color"`
	Owner *string `json:"owner"`
}

// pageFolderMoveRequest is the body of add/move and of remove. The fences are
// pointers so an omitted one is a 400 rather than a zero that happens to
// match a page that has never moved.
type pageFolderMoveRequest struct {
	Page          string `json:"page"`
	PagesVersion  *int64 `json:"pages_version"`
	GrantsVersion *int64 `json:"grants_version"`
}

// ── Loading ────────────────────────────────────────────────────────────────

const pageFolderSelect = `
	SELECT f.id, f.slug, f.name, COALESCE(f.icon, ''), COALESCE(f.color, ''),
	       f.owner_crew_id, COALESCE(c.slug, f.owner_crew_id), COALESCE(c.name, ''),
	       f.grants_version, f.created_at, f.updated_at
	FROM page_folders f LEFT JOIN crews c ON c.id = f.owner_crew_id`

func scanPageFolder(row interface{ Scan(...any) error }) (*pageFolderRecord, error) {
	var f pageFolderRecord
	if err := row.Scan(&f.ID, &f.Slug, &f.Name, &f.Icon, &f.Color,
		&f.OwnerCrewID, &f.OwnerCrewSlug, &f.OwnerCrewName,
		&f.GrantsVersion, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	return &f, nil
}

// loadFoldersIn is every folder in the workspace, keyed by id, in ONE
// statement — the index calls it for the whole workspace.
func (h *PageHandler) loadFoldersIn(ctx context.Context, wsID string) (map[string]*pageFolderRecord, error) {
	rows, err := h.db.QueryContext(ctx, pageFolderSelect+` WHERE f.workspace_id = ?`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*pageFolderRecord{}
	for rows.Next() {
		f, err := scanPageFolder(rows)
		if err != nil {
			return nil, err
		}
		out[f.ID] = f
	}
	return out, rows.Err()
}

func (h *PageHandler) loadFolder(ctx context.Context, wsID, slug string) (*pageFolderRecord, error) {
	return scanPageFolder(h.db.QueryRowContext(ctx, pageFolderSelect+` WHERE f.workspace_id = ? AND f.slug = ?`, wsID, slug))
}

// loadFolderByID is the loader a page's folder_id needs. The workspace is
// not re-checked: the id came from a pages row already scoped to it.
func (h *PageHandler) loadFolderByID(ctx context.Context, id string) (*pageFolderRecord, error) {
	return scanPageFolder(h.db.QueryRowContext(ctx, pageFolderSelect+` WHERE f.id = ?`, id))
}

// folderRefFor is the single-page read's folder, for pageDocumentFor. One
// statement, and only when the page is filed.
func (h *PageHandler) folderRefFor(ctx context.Context, rec *pageRecord) *pageFolderRef {
	if rec.FolderID == "" {
		return nil
	}
	f, err := h.loadFolderByID(ctx, rec.FolderID)
	if err != nil {
		// A miss means the folder row is gone (RESTRICT makes that a race,
		// not a state), and a page whose folder is gone renders as unfiled
		// rather than as an error.
		return nil
	}
	return f.ref()
}

// sortedFolders renders a folder map in the order the rail shows it: by name,
// case-insensitively, then slug.
func sortedFolders(folders map[string]*pageFolderRecord) []*pageFolderRecord {
	out := make([]*pageFolderRecord, 0, len(folders))
	for _, f := range folders {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// ── Standing ───────────────────────────────────────────────────────────────

// folderStanding answers "may this viewer read the folder regardless of what
// is in it": an admin, or a member of the owning crew.
func folderStanding(viewer *pageViewer, f *pageFolderRecord) bool {
	return canRole(viewer.Role, "manage") || viewer.Crews[f.OwnerCrewID]
}

// mayAdministerFolder answers rename, delete and — as the ACCEPTING half of a
// move — add: an admin, or a workspace MANAGER+ who belongs to the owning
// crew. The crew's own MANAGER+ is the crew acting (§5/1).
func mayAdministerFolder(viewer *pageViewer, f *pageFolderRecord) bool {
	if canRole(viewer.Role, "manage") {
		return true
	}
	return canRole(viewer.Role, "create") && viewer.Crews[f.OwnerCrewID]
}

func folderAdminRefusal(verb string, f *pageFolderRecord) string {
	return fmt.Sprintf("%s folder %q needs a workspace admin, or a MANAGER (or higher) who belongs to crew/%s, its owning crew",
		verb, f.Slug, f.OwnerCrewSlug)
}

// folderVisibleTo decides 404 versus 403 for a caller refused a write: a
// folder the caller could not read either answers as if it did not exist.
func (h *PageHandler) folderVisibleTo(ctx context.Context, wsID string, viewer *pageViewer, f *pageFolderRecord) (bool, error) {
	if folderStanding(viewer, f) {
		return true, nil
	}
	index, err := h.loadPageIndex(ctx, wsID, viewer, pagesInFolder(f.ID))
	if err != nil {
		return false, err
	}
	return len(index.rows) > 0, nil
}

// folderOnPath loads the folder named on the path; a missing one is a 404.
func (h *PageHandler) folderOnPath(w http.ResponseWriter, r *http.Request, wsID string) (*pageFolderRecord, bool) {
	slug := r.PathValue("slug")
	f, err := h.loadFolder(r.Context(), wsID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, fmt.Sprintf("folder %q not found", slug))
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load page folder", err)
		return nil, false
	}
	return f, true
}

// folderOrNotFound is folderOnPath plus the read rule: a folder this caller
// reaches nothing in and has no standing on answers as if it did not exist.
// The folder's own reads and writes go through here. The two membership
// endpoints do NOT: a page owner filing their page is told who accepts it
// (§4's sentence) rather than that the folder does not exist, and an owner
// taking a page back has, by definition, reached it.
func (h *PageHandler) folderOrNotFound(w http.ResponseWriter, r *http.Request, wsID string, viewer *pageViewer) (*pageFolderRecord, bool) {
	f, ok := h.folderOnPath(w, r, wsID)
	if !ok {
		return nil, false
	}
	slug := f.Slug
	visible, err := h.folderVisibleTo(r.Context(), wsID, viewer, f)
	if err != nil {
		replyInternalError(w, h.logger, "resolve page folder reach", err)
		return nil, false
	}
	if !visible {
		replyError(w, http.StatusNotFound, fmt.Sprintf("folder %q not found", slug))
		return nil, false
	}
	return f, true
}

func (h *PageHandler) folderViewer(w http.ResponseWriter, r *http.Request) (*AuthUser, string, *pageViewer, bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return nil, "", nil, false
	}
	wsID := WorkspaceIDFromContext(r.Context())
	viewer, err := h.loadViewer(r.Context(), wsID, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page viewer", err)
		return nil, "", nil, false
	}
	return user, wsID, viewer, true
}

// ── 1. List — GET /api/v1/page-folders ─────────────────────────────────────

// ListFolders returns the folders this caller may read, each with the count
// of pages in it the caller reaches. It runs the same bulk loads the page
// index runs and no more, so its cost is the index's whatever the folder
// count.
func (h *PageHandler) ListFolders(w http.ResponseWriter, r *http.Request) {
	_, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	index, err := h.loadPageIndex(r.Context(), wsID, viewer, pagesInWorkspace(wsID))
	if err != nil {
		replyInternalError(w, h.logger, "list page folders", err)
		return
	}
	counts := map[string]int{}
	for i := range index.rows {
		if id := index.rows[i].rec.FolderID; id != "" {
			counts[id]++
		}
	}
	out := make([]pageFolderWire, 0, len(index.folders))
	for _, f := range sortedFolders(index.folders) {
		if counts[f.ID] == 0 && !folderStanding(viewer, f) {
			continue
		}
		out = append(out, f.wire(counts[f.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

// ── 2. Show — GET /api/v1/page-folders/{slug} ──────────────────────────────

func (h *PageHandler) GetFolder(w http.ResponseWriter, r *http.Request) {
	_, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	slug := r.PathValue("slug")
	f, err := h.loadFolder(r.Context(), wsID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, fmt.Sprintf("folder %q not found", slug))
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load page folder", err)
		return
	}
	index, err := h.loadPageIndex(r.Context(), wsID, viewer, pagesInFolder(f.ID))
	if err != nil {
		replyInternalError(w, h.logger, "load page folder pages", err)
		return
	}
	if len(index.rows) == 0 && !folderStanding(viewer, f) {
		replyError(w, http.StatusNotFound, fmt.Sprintf("folder %q not found", slug))
		return
	}
	out := pageFolderShowWire{pageFolderWire: f.wire(len(index.rows)), Pages: make([]pageListWire, 0, len(index.rows))}
	for i := range index.rows {
		out.Pages = append(out.Pages, h.pageListRow(&index.rows[i], index.folders, viewer))
	}
	writeJSON(w, http.StatusOK, out)
}

// ── 3. Create — POST /api/v1/page-folders ──────────────────────────────────

func (h *PageHandler) CreateFolder(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	req, ok := h.decodeFolderWrite(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(strVal(req.Name))
	if name == "" {
		replyError(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > pageFolderMaxNameRunes {
		replyError(w, http.StatusBadRequest, fmt.Sprintf("name is longer than %d characters", pageFolderMaxNameRunes))
		return
	}
	slug := strings.TrimSpace(strVal(req.Slug))
	if slug == "" {
		slug = makeSlug(name)
	}
	if !pageFolderSlugRE.MatchString(slug) {
		replyError(w, http.StatusBadRequest, fmt.Sprintf("slug %q must be lower-case letters, digits, '-' or '_', 64 characters at most, starting with a letter or digit", slug))
		return
	}
	icon, color := strings.TrimSpace(strVal(req.Icon)), strings.TrimSpace(strVal(req.Color))
	if !validPageFolderIcon(icon) {
		replyError(w, http.StatusBadRequest, pageFolderIconRefusal(icon))
		return
	}
	if !validPageFolderColor(color) {
		replyError(w, http.StatusBadRequest, pageFolderColorRefusal(color))
		return
	}
	// The owner is a crew, always, named the way a page names its owner.
	kind, ref, _ := strings.Cut(strings.TrimSpace(strVal(req.Owner)), "/")
	if kind != "crew" || ref == "" {
		replyError(w, http.StatusBadRequest, `owner is required and must be "crew/<slug>": a folder is owned by a crew, never by a person`)
		return
	}
	var crewID, crewName string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, name FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, ref).Scan(&crewID, &crewName)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusBadRequest, fmt.Sprintf("owner crew/%s does not exist in this workspace", ref))
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve page folder owner crew", err)
		return
	}
	// §4: an admin, or a MANAGER+ who is a member of that crew. A MEMBER, and
	// a MANAGER who is not in the crew, are both refused — the crew owns the
	// folder from birth, so the creator has to be able to act for it.
	if !canRole(viewer.Role, "manage") && !(canRole(viewer.Role, "create") && viewer.Crews[crewID]) {
		replyError(w, http.StatusForbidden, fmt.Sprintf(
			"creating a folder owned by crew/%s needs a workspace admin, or a MANAGER (or higher) who belongs to that crew", ref))
		return
	}

	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	id := generateCUID()
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO page_folders (id, workspace_id, slug, name, icon, color, owner_crew_id, grants_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, 0, ?, ?)`,
		id, wsID, slug, name, icon, color, crewID, now, now); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, fmt.Sprintf("a folder with slug %q already exists in this workspace", slug))
			return
		}
		replyInternalError(w, h.logger, "insert page folder", err)
		return
	}
	f := &pageFolderRecord{ID: id, Slug: slug, Name: name, Icon: icon, Color: color,
		OwnerCrewID: crewID, OwnerCrewSlug: ref, OwnerCrewName: crewName, CreatedAt: now, UpdatedAt: now}
	h.journalFolderChange(r.Context(), wsID, user, f, "created", nil)
	broadcastWorkspaceEvent(h.hub, wsID, pageFolderEvent, map[string]any{"slug": f.Slug, "folder_id": f.ID})
	writeJSON(w, http.StatusCreated, f.wire(0))
}

// ── 4. Update — PATCH /api/v1/page-folders/{slug} ──────────────────────────

// UpdateFolder changes the name, icon or colour. An empty string clears the
// icon or the colour; an omitted field is left alone.
func (h *PageHandler) UpdateFolder(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, ok := h.folderOrNotFound(w, r, wsID, viewer)
	if !ok {
		return
	}
	if !mayAdministerFolder(viewer, f) {
		replyError(w, http.StatusForbidden, folderAdminRefusal("changing", f))
		return
	}
	req, ok := h.decodeFolderWrite(w, r)
	if !ok {
		return
	}
	if req.Slug != nil || req.Owner != nil {
		replyError(w, http.StatusBadRequest, "a folder's slug and owner are fixed at creation; neither can be changed here")
		return
	}
	if req.Name == nil && req.Icon == nil && req.Color == nil {
		replyError(w, http.StatusBadRequest, "nothing to change: send name, icon or color")
		return
	}
	changed := map[string]any{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			replyError(w, http.StatusBadRequest, "name cannot be empty")
			return
		}
		if utf8.RuneCountInString(name) > pageFolderMaxNameRunes {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("name is longer than %d characters", pageFolderMaxNameRunes))
			return
		}
		f.Name = name
		changed["name"] = name
	}
	if req.Icon != nil {
		icon := strings.TrimSpace(*req.Icon)
		if !validPageFolderIcon(icon) {
			replyError(w, http.StatusBadRequest, pageFolderIconRefusal(icon))
			return
		}
		f.Icon = icon
		changed["icon"] = icon
	}
	if req.Color != nil {
		color := strings.TrimSpace(*req.Color)
		if !validPageFolderColor(color) {
			replyError(w, http.StatusBadRequest, pageFolderColorRefusal(color))
			return
		}
		f.Color = color
		changed["color"] = color
	}
	f.UpdatedAt = h.evaluator().Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
		UPDATE page_folders SET name = ?, icon = NULLIF(?, ''), color = NULLIF(?, ''), updated_at = ?
		WHERE id = ?`, f.Name, f.Icon, f.Color, f.UpdatedAt, f.ID); err != nil {
		replyInternalError(w, h.logger, "update page folder", err)
		return
	}
	h.journalFolderChange(r.Context(), wsID, user, f, "renamed", changed)
	broadcastWorkspaceEvent(h.hub, wsID, pageFolderEvent, map[string]any{"slug": f.Slug, "folder_id": f.ID})

	index, err := h.loadPageIndex(r.Context(), wsID, viewer, pagesInFolder(f.ID))
	if err != nil {
		replyInternalError(w, h.logger, "count page folder pages", err)
		return
	}
	writeJSON(w, http.StatusOK, f.wire(len(index.rows)))
}

// ── 5. Delete — DELETE /api/v1/page-folders/{slug} ─────────────────────────

func (h *PageHandler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, ok := h.folderOrNotFound(w, r, wsID, viewer)
	if !ok {
		return
	}
	if !mayAdministerFolder(viewer, f) {
		replyError(w, http.StatusForbidden, folderAdminRefusal("deleting", f))
		return
	}
	// Empty means EMPTY, not "empty as far as you can see" (§1/9): the count
	// is over every page, and the refusal does not say how many there are,
	// because that number is exactly what §5/12 keeps from a caller who does
	// not reach them.
	var n int
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM pages WHERE folder_id = ?`, f.ID).Scan(&n); err != nil {
		replyInternalError(w, h.logger, "count page folder pages", err)
		return
	}
	if n > 0 {
		replyError(w, http.StatusConflict, fmt.Sprintf(
			"folder %q is not empty; a folder is deleted only when no page is in it, including pages you cannot see — move them out first", f.Slug))
		return
	}
	// The row's own ON DELETE RESTRICT covers the page filed between the
	// count and the delete: the statement fails rather than orphaning it.
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM page_folders WHERE id = ?`, f.ID); err != nil {
		if isForeignKeyViolation(err) {
			replyError(w, http.StatusConflict, fmt.Sprintf("folder %q is not empty; move its pages out first", f.Slug))
			return
		}
		replyInternalError(w, h.logger, "delete page folder", err)
		return
	}
	h.journalFolderChange(r.Context(), wsID, user, f, "deleted", nil)
	broadcastWorkspaceEvent(h.hub, wsID, pageFolderEvent, map[string]any{"slug": f.Slug, "folder_id": f.ID})
	w.WriteHeader(http.StatusNoContent)
}

// ── 6. Add / move — POST /api/v1/page-folders/{slug}/pages ─────────────────

// AddFolderPage files a page in this folder, moving it out of whichever
// folder it was in. The caller is BOTH initiator and acceptor (§4, v1) or
// the answer is a 403 that says who can.
func (h *PageHandler) AddFolderPage(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, ok := h.folderOnPath(w, r, wsID)
	if !ok {
		return
	}
	req, ok := h.decodeFolderMove(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Page) == "" {
		replyError(w, http.StatusBadRequest, "page is required: the slug of the page to file here")
		return
	}
	if req.PagesVersion == nil || req.GrantsVersion == nil {
		replyError(w, http.StatusBadRequest,
			"pages_version and grants_version are required: a move is confirmed against the page and the folder as you last read them")
		return
	}
	rec, ok := h.folderPageOrNotFound(w, r, wsID, viewer, strings.TrimSpace(req.Page))
	if !ok {
		return
	}
	// Both authorities, in the order a person would ask: may you move this
	// page at all, and may you put it here.
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(viewer.Role, "manage") {
		replyError(w, http.StatusForbidden, fmt.Sprintf(
			"moving page %q is initiated by its owner or a workspace admin; you are neither", rec.Slug))
		return
	}
	if !mayAdministerFolder(viewer, f) {
		replyError(w, http.StatusForbidden, fmt.Sprintf(
			"folder %q accepts a page from a workspace admin, or from a MANAGER (or higher) who belongs to crew/%s, its owning crew; you may move the page, but not into this folder",
			f.Slug, f.OwnerCrewSlug))
		return
	}
	if conflict := h.folderFenceConflict(rec, f, *req.PagesVersion, *req.GrantsVersion); conflict != "" {
		writeFolderFenceConflict(w, rec, f, conflict)
		return
	}
	if rec.FolderID == f.ID {
		// Already here. Nothing changed, so nothing bumps and nothing is
		// journalled; the row is still the answer.
		h.replyFolderPage(w, r, wsID, viewer, rec)
		return
	}
	from := ""
	if rec.FolderID != "" {
		if prev, err := h.loadFolderByID(r.Context(), rec.FolderID); err == nil {
			from = prev.Slug
		}
	}
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE pages SET folder_id = ?, pages_version = pages_version + 1
		WHERE id = ? AND pages_version = ?`, f.ID, rec.ID, rec.PagesVersion)
	if err != nil {
		replyInternalError(w, h.logger, "move page into folder", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Raced by another move between the read and the write: the fence
		// moved under us. Re-read and say so, the same way a stale request
		// is told.
		if fresh, err := h.loadPage(r.Context(), wsID, rec.Slug); err == nil {
			rec = fresh
		}
		writeFolderFenceConflict(w, rec, f, "pages_version")
		return
	}
	rec.FolderID = f.ID
	rec.PagesVersion++
	h.journalFolderMembership(r.Context(), wsID, user, rec, "moved", from, f.Slug)
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{"page_id": rec.ID, "slug": rec.Slug})
	h.replyFolderPage(w, r, wsID, viewer, rec)
}

// ── 7. Remove — DELETE /api/v1/page-folders/{slug}/pages/{page} ────────────

// RemoveFolderPage takes a page out of this folder. The page's owner or an
// admin, and nobody else — the folder is not consulted (§1/9).
func (h *PageHandler) RemoveFolderPage(w http.ResponseWriter, r *http.Request) {
	user, wsID, viewer, ok := h.folderViewer(w, r)
	if !ok {
		return
	}
	f, ok := h.folderOnPath(w, r, wsID)
	if !ok {
		return
	}
	req, ok := h.decodeFolderMove(w, r)
	if !ok {
		return
	}
	if req.PagesVersion == nil {
		replyError(w, http.StatusBadRequest, "pages_version is required: a removal is confirmed against the page as you last read it")
		return
	}
	rec, ok := h.folderPageOrNotFound(w, r, wsID, viewer, r.PathValue("page"))
	if !ok {
		return
	}
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(viewer.Role, "manage") {
		replyError(w, http.StatusForbidden, fmt.Sprintf(
			"removing page %q from its folder is the page owner's or a workspace admin's to do; the folder has no say and neither does anyone else", rec.Slug))
		return
	}
	if rec.PagesVersion != *req.PagesVersion || rec.FolderID != f.ID {
		// A page that is not in this folder is a page the client's view of
		// has gone stale, which is what the fence exists to say.
		writeFolderFenceConflict(w, rec, f, "pages_version")
		return
	}
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE pages SET folder_id = NULL, pages_version = pages_version + 1
		WHERE id = ? AND pages_version = ? AND folder_id = ?`, rec.ID, rec.PagesVersion, f.ID)
	if err != nil {
		replyInternalError(w, h.logger, "remove page from folder", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if fresh, err := h.loadPage(r.Context(), wsID, rec.Slug); err == nil {
			rec = fresh
		}
		writeFolderFenceConflict(w, rec, f, "pages_version")
		return
	}
	rec.FolderID = ""
	rec.PagesVersion++
	h.journalFolderMembership(r.Context(), wsID, user, rec, "removed", f.Slug, "")
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{"page_id": rec.ID, "slug": rec.Slug})
	h.replyFolderPage(w, r, wsID, viewer, rec)
}

// ── Shared pieces ──────────────────────────────────────────────────────────

// folderPageOrNotFound loads a page by slug and answers the same 404 for a
// page that does not exist and one this caller cannot reach (pageReachRule).
func (h *PageHandler) folderPageOrNotFound(w http.ResponseWriter, r *http.Request, wsID string, viewer *pageViewer, slug string) (*pageRecord, bool) {
	rec, err := h.loadPage(r.Context(), wsID, slug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load page", err)
		return nil, false
	}
	panels, err := h.loadPanels(r.Context(), wsID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page panels", err)
		return nil, false
	}
	reachable, err := h.canSeePage(r.Context(), wsID, rec, panels, viewer)
	if err != nil {
		replyInternalError(w, h.logger, "resolve page reach", err)
		return nil, false
	}
	if !reachable {
		replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
		return nil, false
	}
	return rec, true
}

// folderFenceConflict names the first stale fence, or "" when both hold.
func (h *PageHandler) folderFenceConflict(rec *pageRecord, f *pageFolderRecord, pagesVersion, grantsVersion int64) string {
	if rec.PagesVersion != pagesVersion {
		return "pages_version"
	}
	if f.GrantsVersion != grantsVersion {
		return "grants_version"
	}
	return ""
}

// writeFolderFenceConflict is the 409 a stale fence gets: which fence, and
// the CURRENT pair, so the client re-reads and asks again rather than
// retrying blind.
func writeFolderFenceConflict(w http.ResponseWriter, rec *pageRecord, f *pageFolderRecord, conflict string) {
	what := "the page's folder membership"
	if conflict == "grants_version" {
		what = "the folder's grants"
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":          fmt.Sprintf("%s changed since you read it; re-read and confirm again (%s is stale)", what, conflict),
		"conflict":       conflict,
		"pages_version":  rec.PagesVersion,
		"grants_version": f.GrantsVersion,
	})
}

// replyFolderPage answers a move or removal with the page's index row, as
// the listing would now render it for this caller.
func (h *PageHandler) replyFolderPage(w http.ResponseWriter, r *http.Request, wsID string, viewer *pageViewer, rec *pageRecord) {
	index, err := h.loadPageIndex(r.Context(), wsID, viewer, pageOnly(rec.ID))
	if err != nil {
		replyInternalError(w, h.logger, "render moved page", err)
		return
	}
	if len(index.rows) == 0 {
		// Reach was checked before the write and a move changes no access,
		// so this is unreachable in practice; the 404 keeps the invariant
		// rather than inventing a row.
		replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", rec.Slug))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"page": h.pageListRow(&index.rows[0], index.folders, viewer)})
}

func (h *PageHandler) decodeFolderWrite(w http.ResponseWriter, r *http.Request) (*pageFolderWriteRequest, bool) {
	body, ok := readCapped(w, r, pages.MaxSpecBytes, "folder request")
	if !ok {
		return nil, false
	}
	var req pageFolderWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	return &req, true
}

func (h *PageHandler) decodeFolderMove(w http.ResponseWriter, r *http.Request) (*pageFolderMoveRequest, bool) {
	body, ok := readCapped(w, r, pages.MaxSpecBytes, "folder page request")
	if !ok {
		return nil, false
	}
	var req pageFolderMoveRequest
	if len(strings.TrimSpace(string(body))) == 0 {
		return &req, true
	}
	if err := json.Unmarshal(body, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	return &req, true
}

// isForeignKeyViolation is isUniqueViolation's sibling for the one place a
// RESTRICT can fire between a count and a delete.
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "FOREIGN KEY")
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// journalFolderChange follows journalGrantChange: actor and subject in the
// payload, a one-line summary a person can read.
func (h *PageHandler) journalFolderChange(ctx context.Context, wsID string, actor *AuthUser, f *pageFolderRecord, op string, changed map[string]any) {
	if h.journal == nil {
		return
	}
	payload := map[string]any{
		"folder":        f.Slug,
		"folder_id":     f.ID,
		"op":            op,
		"owner":         "crew/" + f.OwnerCrewSlug,
		"actor_user_id": actor.ID,
	}
	for k, v := range changed {
		payload[k] = v
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        entryPageFolderChanged,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actor.ID,
		Summary:     fmt.Sprintf("%s %s page folder %s (crew/%s)", actor.Email, op, f.Slug, f.OwnerCrewSlug),
		Payload:     payload,
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: folder change was not journalled", "folder", f.Slug, "op", op, "error", err)
	}
}

func (h *PageHandler) journalFolderMembership(ctx context.Context, wsID string, actor *AuthUser, rec *pageRecord, op, from, to string) {
	if h.journal == nil {
		return
	}
	summary := fmt.Sprintf("%s moved page %s into folder %s", actor.Email, rec.Slug, to)
	if op == "removed" {
		summary = fmt.Sprintf("%s removed page %s from folder %s", actor.Email, rec.Slug, from)
	} else if from != "" {
		summary = fmt.Sprintf("%s moved page %s from folder %s into folder %s", actor.Email, rec.Slug, from, to)
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        entryPageFolderMembershipChanged,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actor.ID,
		Summary:     summary,
		Payload: map[string]any{
			"page": rec.Slug, "page_id": rec.ID, "op": op, "from": from, "to": to,
			"pages_version": rec.PagesVersion, "actor_user_id": actor.ID,
		},
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: folder membership change was not journalled", "page", rec.Slug, "op", op, "error", err)
	}
}
