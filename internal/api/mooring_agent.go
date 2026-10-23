package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type mooringMandate struct {
	MandateID  string `json:"mandate_id"`
	RunID      string `json:"run_id"`
	BaseRev    int64  `json:"base_rev"`
	Generation int64  `json:"generation"`
	Command    string `json:"command"`
}

func (m *mooringHTTP) mandate(w http.ResponseWriter, r *http.Request) {
	var req mooringMandate
	if decodeGridCraftBody(w, r, &req) != nil || req.MandateID == "" || len(req.MandateID) > 128 || strings.HasPrefix(req.MandateID, "human-") || req.RunID == "" || len(req.RunID) > 128 {
		mooringError(w, mooring.ErrInvalid)
		return
	}
	tx, e := m.begin(r)
	if e != nil {
		mooringError(w, e)
		return
	}
	defer tx.Rollback()
	k, actor, e := m.human(r, tx, true)
	if e != nil {
		mooringError(w, e)
		return
	}
	manifest, state, e := m.document(r.Context(), tx, k)
	if e != nil {
		mooringError(w, e)
		return
	}
	declared := false
	for _, c := range manifest.Commands {
		if c.Name == req.Command {
			declared = true
		}
	}
	if !declared {
		mooringError(w, mooring.ErrInvalid)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	a, e := m.workbook.run(ctx, tx, req.RunID, k.WorkspaceID)
	if e == sql.ErrNoRows {
		e = m.workbook.retainOrdinaryHumanRun(r, tx, req.RunID, k.WorkspaceID, actor)
		if e == nil {
			a, e = m.workbook.run(ctx, tx, req.RunID, k.WorkspaceID)
		}
	}
	if e != nil || actor != "user:"+a.HumanUserID || m.workbook.pages.checkArtifactRunPrincipal(ctx, &a) != nil || !m.workbook.pages.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		mooringError(w, appengine.ErrForbidden)
		return
	}
	var sealed int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND visibility='private' AND (private_artifact_author_user_id IS NULL OR private_artifact_author_user_id=?)`, a.ChatID, a.WorkspaceID, a.HumanUserID, a.HumanUserID).Scan(&sealed) != nil || sealed != 1 {
		mooringError(w, appengine.ErrForbidden)
		return
	}
	var version, policy int64
	if tx.QueryRowContext(ctx, `SELECT pages_version FROM pages WHERE id=?`, k.PageID).Scan(&version) != nil || tx.QueryRowContext(ctx, `SELECT version FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1`, k.WorkspaceID).Scan(&policy) != nil {
		mooringError(w, appengine.ErrForbidden)
		return
	}
	raw, _ := json.Marshal(req)
	fingerprint := mooring.Fingerprint(raw)
	var old string
	e = tx.QueryRowContext(ctx, `SELECT fingerprint FROM mooring_intents WHERE page_id=? AND mandate_id=? AND revoked_at IS NULL`, k.PageID, req.MandateID).Scan(&old)
	if e == nil {
		if old != fingerprint {
			mooringError(w, mooring.ErrConflict)
			return
		}
	} else {
		if e != sql.ErrNoRows || req.BaseRev != state.Rev || req.Generation != state.Generation {
			mooringError(w, mooring.ErrConflict)
			return
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO mooring_intents(page_id,mandate_id,run_id,human_user_id,page_version,policy_version,generation,base_rev,command,fingerprint) VALUES(?,?,?,?,?,?,?,?,?,?)`, k.PageID, req.MandateID, a.RunID, a.HumanUserID, version, policy, req.Generation, req.BaseRev, req.Command, fingerprint)
		if e != nil {
			mooringError(w, e)
			return
		}
	}
	var notify func()
	if _, e = tx.ExecContext(ctx, `UPDATE chats SET private_artifact_author_user_id=? WHERE id=?`, a.HumanUserID, a.ChatID); e == nil {
		notify, e = m.audit(r, tx, k.WorkspaceID, k.PageID, mooringMandateAdmitted, a.HumanUserID, map[string]any{"mandate_id": req.MandateID, "run_id": req.RunID, "command": req.Command, "base_rev": req.BaseRev, "generation": req.Generation})
	}

	if e == nil {
		key, current, err := m.human(r, tx, true)
		e = err
		if e == nil && (key != k || current != actor) {
			e = appengine.ErrForbidden
		}
	}
	if e == nil {
		_, _, e = m.authorizeAgent(r, tx, &a, mooringAgentPacket{ArtifactID: k.PageID, MandateID: req.MandateID, Action: "read"})
	}
	if e == nil {
		var retained string
		e = tx.QueryRowContext(ctx, `SELECT fingerprint FROM mooring_intents WHERE page_id=? AND mandate_id=? AND revoked_at IS NULL`, k.PageID, req.MandateID).Scan(&retained)
		if e == nil && retained != fingerprint {
			e = appengine.ErrForbidden
		}
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		mooringError(w, e)
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, req)
}
func (m *mooringHTTP) revoke(w http.ResponseWriter, r *http.Request) {
	tx, e := m.begin(r)
	if e != nil {
		mooringError(w, e)
		return
	}
	defer tx.Rollback()
	k, actor, e := m.human(r, tx, true)
	if e == nil {
		_, e = tx.ExecContext(r.Context(), `UPDATE mooring_intents SET revoked_at=? WHERE page_id=? AND mandate_id=? AND revoked_at IS NULL`, tsformat.Format(time.Now()), k.PageID, r.PathValue("mandate"))
	}
	var notify func()
	if e == nil {
		notify, e = m.audit(r, tx, k.WorkspaceID, k.PageID, mooringMandateRevoked, UserFromContext(r.Context()).ID, map[string]any{"mandate_id": r.PathValue("mandate")})
	}

	if e == nil {
		key, current, err := m.human(r, tx, true)
		e = err
		if e == nil && (key != k || current != actor) {
			e = appengine.ErrForbidden
		}
	}
	if e == nil {
		var active int
		e = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM mooring_intents WHERE page_id=? AND mandate_id=? AND revoked_at IS NULL`, k.PageID, r.PathValue("mandate")).Scan(&active)
		if e == nil && active != 0 {
			e = appengine.ErrForbidden
		}
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		mooringError(w, e)
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, mooringRevoked{Status: "revoked"})
}

type mooringAgentPacket struct {
	Action     string           `json:"action"`
	ArtifactID string           `json:"artifact_id"`
	MandateID  string           `json:"mandate_id"`
	Request    *mooring.Request `json:"request,omitempty"`
}

func (m *mooringHTTP) agent(w http.ResponseWriter, r *http.Request) {
	checked, ok := m.workbook.pages.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	var packet mooringAgentPacket
	if decodeGridCraftBody(w, r, &packet) != nil || packet.ArtifactID == "" || packet.MandateID == "" || (packet.Action != "read" && packet.Action != "command") || (packet.Action == "read" && packet.Request != nil) || (packet.Action == "command" && packet.Request == nil) {
		mooringError(w, mooring.ErrInvalid)
		return
	}
	a, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if a != nil {
		var native bool
		err := m.workbook.pages.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM gridcraft_heads WHERE workspace_id=? AND page_id=?)`, a.WorkspaceID, packet.ArtifactID).Scan(&native)
		if err != nil {
			mooringError(w, err)
			return
		}
		if native {
			m.nativeAgent(w, r, packet)
			return
		}
	}
	if a == nil {
		mooringError(w, appengine.ErrForbidden)
		return
	}
	tx, e := m.begin(r)
	if e != nil {
		mooringError(w, e)
		return
	}
	defer tx.Rollback()
	k, actor, e := m.authorizeAgent(r, tx, a, packet)
	if e != nil {
		mooringError(w, e)
		return
	}
	manifest, state, e := m.document(r.Context(), tx, k)
	if e != nil {
		mooringError(w, e)
		return
	}
	if packet.Action == "read" {
		writeJSON(w, 200, map[string]any{"app_id": manifest.ID, "manifest": manifest, "state": state})
		return
	}
	m.commit(w, r, tx, k, actor, *packet.Request, func(context.Context, *sql.Tx) (string, error) {
		_, current, e := m.authorizeAgent(r, tx, a, packet)
		return current, e
	})
}

// nativeAgent translates the generic command envelope at the adapter boundary.
// The existing native handler still performs every mandate and run recheck.
func (m *mooringHTTP) nativeAgent(w http.ResponseWriter, r *http.Request, packet mooringAgentPacket) {
	native := gridcraftAgentRequest{Action: packet.Action, ArtifactID: packet.ArtifactID, MandateID: packet.MandateID}
	if packet.Action == "command" {
		req := packet.Request
		if req == nil || req.Command != "cell.set" {
			mooringError(w, mooring.ErrInvalid)
			return
		}
		var args struct {
			Sheet string `json:"sheet"`
			Cell  string `json:"cell"`
			Input string `json:"input"`
		}
		manifest, e := mooring.Application("gridcraft")
		if e != nil {
			mooringError(w, e)
			return
		}
		validated, e := manifest.Validate(req.Command, req.Arguments)
		if e != nil || appengine.DecodeStrict(validated, &args) != nil {
			mooringError(w, mooring.ErrInvalid)
			return
		}
		var reads *appengine.ReadSet
		if len(req.ReadSet) != 0 {
			reads = &appengine.ReadSet{}
			if bytes.Equal(bytes.TrimSpace(req.ReadSet), []byte("null")) || appengine.DecodeStrict(req.ReadSet, reads) != nil {
				mooringError(w, mooring.ErrInvalid)
				return
			}
		}
		native.Action = "apply"
		native.Request = &appengine.Request{ReadSet: reads, MutationID: req.MutationID, MandateID: packet.MandateID, BaseRev: req.BaseRev, Generation: req.Generation, Command: appengine.Command{Kind: req.Command, Sheet: args.Sheet, Cell: args.Cell, Input: args.Input}}
	}
	raw, _ := json.Marshal(native)
	request := r.Clone(r.Context())
	request.Body = io.NopCloser(bytes.NewReader(raw))
	request.ContentLength = int64(len(raw))

	if packet.Action == "read" {
		captured := &mooringNativeResponse{header: make(http.Header)}
		m.workbook.agent(captured, request)
		if captured.status == http.StatusOK {
			var state appengine.State
			if json.Unmarshal(captured.body.Bytes(), &state) != nil {
				mooringError(w, mooring.ErrInvalid)
				return
			}
			manifest, e := mooring.Application("gridcraft")
			if e != nil {
				mooringError(w, e)
				return
			}
			writeJSON(w, 200, map[string]any{"app_id": manifest.ID, "manifest": manifest, "state": state})
			return
		}
		for k, values := range captured.header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(captured.status)
		_, _ = w.Write(captured.body.Bytes())
		return
	}
	m.workbook.agent(w, request)
}

type mooringNativeResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *mooringNativeResponse) Header() http.Header { return w.header }
func (w *mooringNativeResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *mooringNativeResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(p)
}

func (m *mooringHTTP) authorizeAgent(r *http.Request, tx *sql.Tx, a *artifactRunAuthority, packet mooringAgentPacket) (appengine.Key, string, error) {
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	if m.workbook.pages.checkArtifactRunPrincipal(ctx, a) != nil || !m.workbook.pages.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return appengine.Key{}, "", appengine.ErrForbidden
	}
	role, e := m.workbook.pages.liveContentHumanRole(ctx, a.WorkspaceID, a.HumanUserID)
	if e != nil {
		return appengine.Key{}, "", appengine.ErrForbidden
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	rec, e := m.workbook.pages.loadPage(ctx, a.WorkspaceID, "@"+packet.ArtifactID)
	if e != nil || rec.OwnerUserID != a.HumanUserID || rec.OwnerCrewID != "" || rec.FolderID != "" || !rec.PrivateAudience || rec.HasWorkbook || !m.workbook.pages.mayEditSpec(ctx, a.WorkspaceID, a.HumanUserID, role, rec) {
		return appengine.Key{}, "", appengine.ErrForbidden
	}
	k := appengine.Key{WorkspaceID: a.WorkspaceID, PageID: packet.ArtifactID}
	var version, policy, generation, base int64
	var command string
	e = tx.QueryRowContext(ctx, `SELECT page_version,policy_version,generation,base_rev,command FROM mooring_intents WHERE page_id=? AND mandate_id=? AND run_id=? AND human_user_id=? AND revoked_at IS NULL`, k.PageID, packet.MandateID, a.RunID, a.HumanUserID).Scan(&version, &policy, &generation, &base, &command)
	var enabled bool
	if e != nil || version != rec.PagesVersion || tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1 AND version=?)`, k.WorkspaceID, policy).Scan(&enabled) != nil || !enabled {
		return appengine.Key{}, "", appengine.ErrForbidden
	}
	_, state, e := m.document(ctx, tx, k)
	if e != nil {
		return appengine.Key{}, "", e
	}
	if state.Generation != generation {
		return appengine.Key{}, "", appengine.ErrForbidden
	}

	if packet.Request != nil && (packet.Request.Command != command || packet.Request.Generation != generation || packet.Request.BaseRev != base) {
		return appengine.Key{}, "", appengine.ErrForbidden
	}
	return k, "agent:" + a.AgentID + ":run:" + a.RunID + ":mandate:" + packet.MandateID, nil
}
