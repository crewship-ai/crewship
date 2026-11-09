package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
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
	// The picker asks on every keystroke; each person has a bounded budget.
	if user := UserFromContext(r.Context()); user != nil {
		if wait := h.documentPeople().take(WorkspaceIDFromContext(r.Context()), user.ID); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeJSON(w, 429, map[string]string{"error": "too many people lookups; wait and retry", "code": "document_people_rate_limit"})
			return
		}
	}
	// Resolves the Page and answers 503/404 the way the document read does;
	// the record itself is read again inside the snapshot below.
	if _, ok := h.documentPage(w, r); !ok {
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
	rec, err := h.loadPageForRead(ctx, workspace, r.PathValue("slug"))
	if err != nil {
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
	// The name match and the access filter both run in SQL before the limit,
	// so a reader is found however many non-readers share the search text; at
	// most documentPeopleCheckLimit candidates then get the exact authority
	// check. Unicode case folding, as other name searches do.
	audience, audienceArgs := documentPeopleAudience(rec)
	args := append(append([]any{workspace, search}, audienceArgs...), documentPeopleCheckLimit)
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

// documentPeopleAudience narrows the search to the people who can possibly
// read this Document, in SQL: a superset of documentReader that the exact
// check then decides. Private output has only its custodian and the workspace
// administrators. Any other Document page: unrestricted members who are
// OWNER/ADMIN, its owner, in its owning crew, or named, directly or through a
// crew, by a live grant (the same verdict view the grant reader uses: issuer
// in standing, not retired) or by its folder's ACL, or the workspace is named.
// Agent grants never make a person a reader.
func documentPeopleAudience(rec *pageRecord) (string, []any) {
	if rec.PrivateAudience {
		return ` AND (u.id=? OR m.role IN ('OWNER','ADMIN'))`, []any{rec.OwnerUserID}
	}
	const named = `(s.subject_type='workspace' OR (s.subject_type='user' AND s.subject_id=u.id) OR (s.subject_type='crew' AND s.subject_id IN (SELECT cm.crew_id FROM crew_members cm JOIN crews c ON c.id=cm.crew_id AND c.deleted_at IS NULL WHERE cm.user_id=u.id)))`
	return ` AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=m.user_id AND restricted.access_mode='restricted')
AND (m.role IN ('OWNER','ADMIN') OR u.id=?
 OR EXISTS(SELECT 1 FROM crew_members cm JOIN crews c ON c.id=cm.crew_id AND c.deleted_at IS NULL WHERE cm.user_id=u.id AND cm.crew_id=?)
 OR EXISTS(SELECT 1 FROM page_grant_verdicts s WHERE s.page_id=? AND s.issuer_standing=1 AND s.retired_at IS NULL AND s.level IN ('read','comment','produce','write') AND ` + named + `)
 OR EXISTS(SELECT 1 FROM page_folder_acl s WHERE s.folder_id=? AND ` + named + `))`,
		[]any{rec.OwnerUserID, rec.OwnerCrewID, rec.ID, rec.FolderID}
}

// replyDocumentPeople ends the read snapshot first, then records the audited
// administrator read (a write) and only then answers. An administrator's
// role-only lookups are audited once per Document, custodian and credential
// within documentPeopleAuditWindow, not once per keystroke of the picker.
func (h *PageHandler) replyDocumentPeople(w http.ResponseWriter, r *http.Request, tx *sql.Tx, rec *pageRecord, people []documentPerson) {
	_ = tx.Rollback()
	ctx, workspace, user := r.Context(), WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context())
	// Decide first whether this read is audited at all, so the writer is taken
	// only for an audit that will be committed: the custodian and anyone who
	// reaches the Document without the administrator role need none.
	audited, role := false, ""
	if user != nil && user.ID != rec.OwnerUserID {
		var err error
		role, err = h.liveContentHumanRole(ctx, workspace, user.ID)
		if errors.Is(err, sql.ErrNoRows) {
			role, err = "", nil
		}
		if err == nil && workspaceArtifactReader(role) {
			audited = rec.PrivateAudience
			if !audited {
				audited, err = h.reachedOnlyByAdminRole(ctx, workspace, rec, user.ID)
			}
		}
		if err != nil {
			replyInternalError(w, h.logger, "authorize document people audit", err)
			return
		}
	}
	key := documentPeopleAuditKey(r, rec, role)
	if !audited || key == "" {
		if !h.auditPrivateOwnerAccess(w, r, rec) {
			return
		}
		writeJSON(w, 200, documentPeopleResponse{People: people})
		return
	}
	// Already audited for this Document, custodian, role and credential: the
	// role and reach were re-derived above from live state, nothing is written.
	if h.documentPeople().auditedRecently(key) {
		writeJSON(w, 200, documentPeopleResponse{People: people})
		return
	}
	r, b, err := h.startMetadataRead(r, true)
	if err != nil {
		replyInternalError(w, h.logger, "begin document people audit", err)
		return
	}
	defer b.tx.Rollback()
	if !h.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	if !h.finishMetadataRead(w, r, b) {
		return
	}
	if len(b.audits) > 0 {
		h.documentPeople().rememberAudit(key)
	}
	writeJSON(w, 200, documentPeopleResponse{People: people})
}

// documentPeopleAuditKey names one administrator read: workspace, Document,
// its owner and audience, the reader's live role, and the exact credential
// (session or CLI token digest). "" when the credential cannot be named, so
// every lookup is audited.
func documentPeopleAuditKey(r *http.Request, rec *pageRecord, role string) string {
	user := UserFromContext(r.Context())
	if user == nil {
		return ""
	}
	var credential string
	switch AuthKindFromContext(r.Context()) {
	case AuthKindSession:
		if user.SessionID == "" {
			return ""
		}
		credential = "session:" + user.SessionID
	case AuthKindCLIToken:
		token := extractToken(r)
		if token == "" {
			return ""
		}
		sum := sha256.Sum256([]byte(token))
		credential = "cli:" + hex.EncodeToString(sum[:])
	default:
		return ""
	}
	return strings.Join([]string{WorkspaceIDFromContext(r.Context()), user.ID, rec.ID, rec.OwnerUserID, rec.OwnerCrewID, strconv.FormatBool(rec.PrivateAudience), role, credential}, "\x00")
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
