package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// CraftVersionHistoryFlag is the workspace feature flag for native Word and
// Sheet version history. An undefined flag is off.
const CraftVersionHistoryFlag = "craft_version_history"

// craftNativeVersionsLimit bounds one listing in SQL; older versions stay
// stored and restorable, the response only says it was truncated.
const craftNativeVersionsLimit = 200

type craftNativeVersionAuthorResponse struct {
	ActorID     string `json:"actorId"`
	Kind        string `json:"kind"`
	DisplayName string `json:"displayName"`
}

// craftNativeVersionProposalResponse marks a version a person saved by
// accepting an agent proposal: the version's author is that person (the
// approver and writer), the proposal's author is the agent.
type craftNativeVersionProposalResponse struct {
	ID     string                           `json:"id"`
	Author craftNativeVersionAuthorResponse `json:"author"`
}
type craftNativeVersionResponse struct {
	Version      string                              `json:"version"`
	CreatedAt    string                              `json:"createdAt"`
	Author       craftNativeVersionAuthorResponse    `json:"author"`
	RestoredFrom string                              `json:"restoredFrom,omitempty"`
	Proposal     *craftNativeVersionProposalResponse `json:"proposal,omitempty"`
	Current      bool                                `json:"current"`
	// A workbook Sheet's version file: xlsx, xlsm, or native for a version from before workbook files.
	Format      string `json:"format,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
	Size        string `json:"size,omitempty"`
}

// craftNativeVersionDocumentResponse is one historical native version with
// its bytes: the same profile fields as the whole document read, so a client
// can render it read-only in the qualified editor bundle, plus its metadata.
type craftNativeVersionDocumentResponse struct {
	Version        string                              `json:"version"`
	CurrentVersion string                              `json:"currentVersion"`
	Current        bool                                `json:"current"`
	CreatedAt      string                              `json:"createdAt"`
	Author         craftNativeVersionAuthorResponse    `json:"author"`
	RestoredFrom   string                              `json:"restoredFrom,omitempty"`
	Proposal       *craftNativeVersionProposalResponse `json:"proposal,omitempty"`
	ApplicationID  string                              `json:"applicationId"`
	EnginePin      string                              `json:"enginePin"`
	EditorBundle   string                              `json:"editorBundle"`
	EditorURL      string                              `json:"editorURL"`
	Format         string                              `json:"format,omitempty"`
	Bytes          []byte                              `json:"bytes"`
}
type craftNativeVersionsResponse struct {
	CurrentVersion string                       `json:"currentVersion"`
	Versions       []craftNativeVersionResponse `json:"versions"`
	Truncated      bool                         `json:"truncated"`
}

// listVersions is read-only. History is private: it needs the same authority as
// the whole document read, so a person gets exactly the refusal that read
// returns; only then does the workspace flag apply. Restoring stays the fenced
// POST document/restore; there is no second write path.
func (d *craftNativeDocumentsHTTP) listVersions(w http.ResponseWriter, r *http.Request) {
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.craftVersionHistoryReadable(w, r, entry) {
		return
	}
	versions, truncated, err := s.Versions(r.Context(), entry.scope, craftNativeVersionsLimit)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	listed := make([]int64, 0, len(versions))
	for _, v := range versions {
		listed = append(listed, v.Version)
	}
	// Provenance of accepted agent proposals is read under the same live read
	// authority as the listing itself, and before the audit, so a refusal
	// releases nothing.
	origins, err := s.AcceptedProposalOrigins(r.Context(), entry.scope, listed)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	// PRD §5: history (times, authors) is content of the artifact; an
	// administrator's read of someone else's document is audited exactly like
	// the document read, and a failed audit releases nothing.
	if !d.auditRead(w, r, entry) {
		return
	}
	out := craftNativeVersionsResponse{Versions: make([]craftNativeVersionResponse, 0, len(versions)), Truncated: truncated}
	names := map[string]craftNativeVersionAuthorResponse{}
	name := func(actor string) craftNativeVersionAuthorResponse {
		author, ok := names[actor]
		if !ok {
			author = d.versionAuthor(r, entry.scope.WorkspaceID, actor)
			names[actor] = author
		}
		return author
	}
	for i, v := range versions {
		item := craftNativeVersionResponse{Version: strconv.FormatInt(v.Version, 10), CreatedAt: v.CreatedAt, Author: name(v.Actor), Current: i == 0}
		if s.Workbook() {
			item.Format = mooring.WorkbookFormatOf(v.EntryPoint)
			if v.EntryHash != "" {
				item.ContentHash, item.Size = "sha256:"+v.EntryHash, strconv.FormatInt(v.EntrySize, 10)
			}
		}
		if v.RestoredFrom > 0 {
			item.RestoredFrom = strconv.FormatInt(v.RestoredFrom, 10)
		}
		if o, ok := origins[v.Version]; ok {
			item.Proposal = &craftNativeVersionProposalResponse{ID: o.ID, Author: name(o.AuthorActorID)}
		}
		if i == 0 {
			out.CurrentVersion = item.Version
		}
		out.Versions = append(out.Versions, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// craftVersionHistoryReadable refuses a person without private read authority
// before the flag answers. The store repeats the check in its read transaction.
func (d *craftNativeDocumentsHTTP) craftVersionHistoryReadable(w http.ResponseWriter, r *http.Request, entry *craftNativeSession) bool {
	return d.craftNativeHistoryReadable(w, r, entry) && craftVersionHistoryEnabled(w, r, d, entry)
}

// craftNativeHistoryReadable is the read rule of craftVersionHistoryReadable without the history
// feature flag: an editor replaying agent operations needs them whether or not the history is shown.
func (d *craftNativeDocumentsHTTP) craftNativeHistoryReadable(w http.ResponseWriter, r *http.Request, entry *craftNativeSession) bool {
	tx, err := d.human.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		craftNativeDocumentError(w, err)
		return false
	}
	proof, err := d.human.checkEntry(r.Context(), tx, entry, false)
	_ = tx.Rollback()
	if err == nil && !proof.privateRead {
		err = errCraftNativeAccess
	}
	if err != nil {
		craftNativeDocumentError(w, err)
		return false
	}
	return true
}

func craftVersionHistoryEnabled(w http.ResponseWriter, r *http.Request, d *craftNativeDocumentsHTTP, entry *craftNativeSession) bool {
	enabled, err := featureflags.IsEnabled(r.Context(), d.human.pages.db, entry.scope.WorkspaceID, CraftVersionHistoryFlag)
	if err != nil {
		replyError(w, 500, "native version history unavailable")
		return false
	}
	if !enabled {
		replyError(w, 404, "native version history is not enabled")
		return false
	}
	return true
}

// readVersion returns the bytes of one saved native version. It is the
// authorized read-only diagnostic and the preview/compare source: the same
// admission, live private read authority, immutable profile check and
// administrator audit as the whole document read, behind the same workspace
// flag as the listing. It never acquires a lease, never writes and never moves the shared
// pin; restoring stays the fenced POST document/restore.
func (d *craftNativeDocumentsHTTP) readVersion(w http.ResponseWriter, r *http.Request) {
	r, entry, s, profile, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.craftVersionHistoryReadable(w, r, entry) {
		return
	}
	version, err := craftNativeVersion(r.PathValue("version"))
	if err != nil {
		replyError(w, 400, "invalid version")
		return
	}
	m, latest, err := s.ReadVersion(r.Context(), entry.scope, version)
	if errors.Is(err, artifacts.ErrVersionNotFound) {
		// Authority was checked before the lookup inside the same
		// transaction, so only an authorized reader learns a version is absent.
		replyError(w, 404, "native version not found")
		return
	}
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	origins, err := s.AcceptedProposalOrigins(r.Context(), entry.scope, []int64{m.Version})
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.auditRead(w, r, entry) {
		return
	}
	file, err := s.DocumentFile(m)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	out := craftNativeVersionDocumentResponse{Version: strconv.FormatInt(m.Version, 10), CurrentVersion: strconv.FormatInt(latest, 10), Current: m.Version == latest, CreatedAt: m.CreatedAt, Author: d.versionAuthor(r, entry.scope.WorkspaceID, m.Actor), ApplicationID: entry.application, EnginePin: s.EnginePin(), EditorBundle: profile.Store.EditorBundle, EditorURL: profile.EditorURL, Bytes: file.Content, Format: craftWorkbookResponseFormat(file)}
	if m.RestoredFrom > 0 {
		out.RestoredFrom = strconv.FormatInt(m.RestoredFrom, 10)
	}
	if o, ok := origins[m.Version]; ok {
		out.Proposal = &craftNativeVersionProposalResponse{ID: o.ID, Author: d.versionAuthor(r, entry.scope.WorkspaceID, o.AuthorActorID)}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// versionAuthor names a recorded actor the way the lease bar names a holder.
// Agents resolve only within this workspace.
func (d *craftNativeDocumentsHTTP) versionAuthor(r *http.Request, workspaceID, actor string) craftNativeVersionAuthorResponse {
	db := d.human.pages.db
	switch {
	case strings.HasPrefix(actor, "agent:"):
		name := "Agent"
		_ = db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(name,''),'Agent') FROM agents WHERE id=? AND workspace_id=?`, strings.TrimPrefix(actor, "agent:"), workspaceID).Scan(&name)
		return craftNativeVersionAuthorResponse{ActorID: actor, Kind: "agent", DisplayName: name}
	case strings.HasPrefix(actor, "user:"):
		name := "User"
		_ = db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(full_name,''),'User') FROM users WHERE id=?`, strings.TrimPrefix(actor, "user:")).Scan(&name)
		return craftNativeVersionAuthorResponse{ActorID: actor, Kind: "human", DisplayName: name}
	default:
		return craftNativeVersionAuthorResponse{ActorID: actor, Kind: "system", DisplayName: "Crewship"}
	}
}
