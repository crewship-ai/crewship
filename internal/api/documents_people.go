package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
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
	documentPeopleScanLimit    = 1000
	documentPeopleQueryLimit   = 100
	documentPeopleNameMaxRunes = 200
)

var documentPersonID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type documentPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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
	if query.Has("ids") && query.Has("q") {
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
	ctx, workspace, user := r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context())
	if user == nil || !rec.HasDocument || rec.HasProject || rec.HasApplication {
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
	if !h.auditPrivateOwnerAccess(w, r, rec) {
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
		writeJSON(w, 200, map[string]any{"people": people})
		return
	}
	rows, err := h.pageAuthReader(ctx).QueryContext(ctx, `SELECT u.id, COALESCE(u.full_name,'') FROM workspace_members m
JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
WHERE m.workspace_id=? ORDER BY lower(COALESCE(u.full_name,'')), u.id LIMIT ?`, workspace, documentPeopleScanLimit)
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
		if documentPersonName(person.Name) != "" && strings.Contains(strings.ToLower(person.Name), strings.ToLower(search)) {
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
	writeJSON(w, 200, map[string]any{"people": people})
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
	name = strings.Join(strings.FieldsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }), " ")
	name = strings.TrimSpace(name)
	if runes := []rune(name); len(runes) > documentPeopleNameMaxRunes {
		name = string(runes[:documentPeopleNameMaxRunes])
	}
	return name
}
