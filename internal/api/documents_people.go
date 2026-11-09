package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

// Document mentions (#5155, schema 5) store only an opaque user id. Names are
// resolved here, per reader and per request, never stored in the content.
//
// A name is released only for a person who can read this Document themselves,
// and only to a caller who can read it too. Every other id, whether it names
// nobody, someone outside the workspace or a member without access, gets the
// same answer: no entry. The reader then shows a neutral "Unknown member", so
// the response never confirms that an id exists.

const (
	documentPeopleIDLimit      = 100
	documentPeopleResultLimit  = 20
	documentPeopleCheckLimit   = 200
	documentPeopleQueryLimit   = 100
	documentPeopleNameMaxRunes = 200
)

var documentPersonID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type documentPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// documentPeopleResponse is the GET …/document/people body.
type documentPeopleResponse struct {
	People []documentPerson `json:"people"`
}

// documentReader mirrors the human read decision of the Document authority
// (documents_authority.go): live, unrestricted membership, then the private
// audience or the ordinary Page reach (role, grants, folders).
func (h *PageHandler) documentReader(ctx context.Context, workspace string, rec *pageRecord, user string) (bool, error) {
	if rec.PrivateAudience {
		viewer, err := h.livePrivatePageReader(ctx, workspace, user)
		if err != nil {
			return false, err
		}
		return privatePageViewer(rec, viewer), nil
	}
	role, err := h.liveContentHumanRole(ctx, workspace, user)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	viewer, err := h.loadViewer(ctx, workspace, user)
	if err != nil {
		return false, err
	}
	viewer.Role = role
	return h.canSeePage(ctx, workspace, rec, nil, viewer)
}

// DocumentPeople answers GET /api/v1/pages/{slug}/document/people.
//
//	?ids=a,b  names for these mention ids (at most 100), for rendering
//	?q=text   people who can read this Document whose name contains text,
//	          for the @-mention picker (at most 20; empty q lists the first 20)
func (h *PageHandler) DocumentPeople(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 403, "human document credentials required")
		return
	}
	query := r.URL.Query()
	if len(query["ids"]) > 1 || len(query["q"]) > 1 || (query.Has("ids") && query.Has("q")) {
		replyError(w, 400, "use either ids or q")
		return
	}
	var ids []string
	if query.Has("ids") {
		seen := map[string]bool{}
		for _, id := range strings.Split(query.Get("ids"), ",") {
			if !documentPersonID.MatchString(id) {
				replyError(w, 400, "invalid person id")
				return
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		if len(ids) > documentPeopleIDLimit {
			replyError(w, 400, "too many person ids")
			return
		}
	}
	search := strings.TrimSpace(query.Get("q"))
	if len([]rune(search)) > documentPeopleQueryLimit {
		replyError(w, 400, "search text too long")
		return
	}
	rec, ok := h.documentPage(w, r)
	if !ok {
		return
	}
	// One read-only snapshot for the credential, the page shape and every authority check.
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyError(w, 503, "document people unavailable")
		return
	}
	defer tx.Rollback()
	outer := r
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	ctx, workspace, user := r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context())
	// Owner and audience come from the same snapshot as every check below.
	if rec, err = h.loadPageForRead(ctx, workspace, r.PathValue("slug")); err != nil {
		replyError(w, 404, "document page not found")
		return
	}
	// The same exact credential and CLI scope the document read requires.
	if err := validateContentHumanCredential(r, tx, "pages:read"); err != nil {
		switch {
		case errors.Is(err, errContentCredentialScope):
			replyError(w, 403, "current pages:read scope required")
		case errors.Is(err, errContentCredentialUnverified):
			replyError(w, 503, "document people unavailable")
		default:
			replyError(w, 401, "current human credential required")
		}
		return
	}
	if user == nil || !rec.HasDocument || rec.HasProject || rec.HasApplication {
		replyError(w, 404, "document page not found")
		return
	}
	// A Page the document read refuses (panels or another output) names nobody either.
	if other, err := documentPageHasOtherOutput(ctx, tx, rec.ID); err != nil {
		replyError(w, 500, "document people unavailable")
		return
	} else if other {
		replyError(w, 404, "document page not found")
		return
	}
	if allowed, err := h.documentReader(ctx, workspace, rec, user.ID); err != nil {
		replyError(w, 500, "document people unavailable")
		return
	} else if !allowed {
		replyError(w, 404, "document page not found")
		return
	}
	people := []documentPerson{}
	if query.Has("ids") {
		for _, id := range ids {
			person, found, err := h.documentPersonFor(ctx, workspace, rec, id)
			if err != nil {
				replyError(w, 500, "document people unavailable")
				return
			}
			if found {
				people = append(people, person)
			}
		}
		h.replyDocumentPeople(w, outer, tx, rec, people)
		return
	}
	// The name match runs in SQL, so every member can be found; at most
	// documentPeopleCheckLimit matching candidates get an authority check.
	// Unicode case folding, as other name searches do; on private output only
	// its custodian and the workspace administrators can be readers at all.
	audience, args := "", []any{workspace, search}
	if rec.PrivateAudience {
		audience = ` AND (u.id=? OR m.role IN ('OWNER','ADMIN'))`
		args = append(args, rec.OwnerUserID)
	}
	args = append(args, documentPeopleCheckLimit)
	rows, err := tx.QueryContext(ctx, `SELECT u.id, COALESCE(u.full_name,'') FROM workspace_members m
JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
WHERE m.workspace_id=? AND trim(COALESCE(u.full_name,''))<>'' AND instr(crewship_casefold(u.full_name), crewship_casefold(?))>0`+audience+`
ORDER BY crewship_casefold(u.full_name), u.id LIMIT ?`, args...)
	if err != nil {
		replyError(w, 500, "document people unavailable")
		return
	}
	var candidates []documentPerson
	for rows.Next() {
		var person documentPerson
		if err := rows.Scan(&person.ID, &person.Name); err != nil {
			rows.Close()
			replyError(w, 500, "document people unavailable")
			return
		}
		if documentPersonName(person.Name) != "" {
			candidates = append(candidates, person)
		}
	}
	rows.Close()
	if rows.Err() != nil {
		replyError(w, 500, "document people unavailable")
		return
	}
	for _, candidate := range candidates {
		if len(people) == documentPeopleResultLimit {
			break
		}
		allowed, err := h.documentReader(ctx, workspace, rec, candidate.ID)
		if err != nil {
			replyError(w, 500, "document people unavailable")
			return
		}
		if allowed {
			people = append(people, documentPerson{ID: candidate.ID, Name: documentPersonName(candidate.Name)})
		}
	}
	h.replyDocumentPeople(w, outer, tx, rec, people)
}

// replyDocumentPeople ends the read snapshot first, then records the audited
// administrator read of private output (a write) and only then answers.
func (h *PageHandler) replyDocumentPeople(w http.ResponseWriter, r *http.Request, tx *sql.Tx, rec *pageRecord, people []documentPerson) {
	_ = tx.Rollback()
	if !h.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	writeJSON(w, 200, documentPeopleResponse{People: people})
}

func (h *PageHandler) documentPersonFor(ctx context.Context, workspace string, rec *pageRecord, id string) (documentPerson, bool, error) {
	allowed, err := h.documentReader(ctx, workspace, rec, id)
	if err != nil || !allowed {
		return documentPerson{}, false, err
	}
	var name string
	err = h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT COALESCE(full_name,'') FROM users WHERE id=? AND suspended_at IS NULL`, id).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return documentPerson{}, false, nil
	}
	if err != nil {
		return documentPerson{}, false, err
	}
	name = documentPersonName(name)
	if name == "" {
		return documentPerson{}, false, nil
	}
	return documentPerson{ID: id, Name: name}, true, nil
}

// documentPersonName is the display name only: one line, bounded, no control
// characters. An empty result means the person is shown as Unknown member.
func documentPersonName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Bidi_Control, r) {
			return -1 // direction overrides and isolates; joiners stay for emoji and scripts
		}
		return r
	}, name)
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp) }), " ")
	name = strings.TrimSpace(name)
	if runes := []rune(name); len(runes) > documentPeopleNameMaxRunes {
		name = string(runes[:documentPeopleNameMaxRunes])
	}
	return name
}

// documentPageHasOtherOutput mirrors the document authority's page shape
// check: a Page with panels or any other installed output type is not a
// Document page, so it is refused the same way here.
func documentPageHasOtherOutput(ctx context.Context, q pageAuthQueryer, pageID string) (bool, error) {
	var panels int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM page_panels WHERE page_id=?", pageID).Scan(&panels); err != nil {
		return false, err
	}
	if panels != 0 {
		return true, nil
	}
	for _, table := range documentExclusiveOutputTables {
		var installed, attached bool
		if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name=? COLLATE NOCASE)", table).Scan(&installed); err != nil {
			return false, err
		}
		if !installed {
			continue
		}
		if err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE page_id=?)", pageID).Scan(&attached); err != nil {
			return false, err
		}
		if attached {
			return true, nil
		}
	}
	return false, nil
}
