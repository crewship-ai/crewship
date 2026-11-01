package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// A view is only an immutable identity/authority fence, never authentication or
// an expiring admission extended in place. Each HTTP operation starts with its
// own normally authenticated request and retains that exact proof to completion.
type commandProductView struct {
	document *commandProductDocument
	original collaborationHumanAdmission
}

type commandProductEditor struct {
	BundleDigest string `json:"bundle_digest"`
	URL          string `json:"url"`
}

type commandProductHTTPResponse struct {
	View       string                `json:"view"`
	Actor      string                `json:"actor"`
	Format     string                `json:"format"`
	Engine     string                `json:"engine"`
	Projection json.RawMessage       `json:"projection"`
	Outcome    *commandSQLOutcome    `json:"outcome"`
	Editor     *commandProductEditor `json:"editor,omitempty"`
}

func commandSameView(original, fresh collaborationHumanAdmission) bool {
	a, b := original.source, fresh.source
	// JWT expiry belongs to each independent HTTP request. A view cannot
	// substitute a different SQL session, user or revocation generation.
	a.ExpiresAt, b.ExpiresAt = "", ""
	return a == b && original.authorVector == fresh.authorVector && original.ingress == fresh.ingress && original.member == fresh.member && original.memberRevision == fresh.memberRevision && original.permission == fresh.permission && original.boot == fresh.boot
}

func commandReleaseAdmission(s *collaborationCommandStore, original collaborationHumanAdmission) {
	s.humans.mu.Lock()
	delete(s.humans.admissions, original.participant)
	s.humans.mu.Unlock()
}

// serve is registered only by explicit trusted product startup wiring. It
// never dispatches a C1 document to the legacy Yjs runtime on any failure.
func (f *commandProductFactory) serve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if controller.SetWriteDeadline(deadline) != nil || controller.SetReadDeadline(deadline) != nil || !collaborationSessionCanFlush(w) {
		replyError(w, 503, "bounded command transport unavailable")
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}); _ = controller.SetReadDeadline(time.Time{}) }()
	if f == nil || !f.enabled || f.manager == nil {
		replyError(w, 503, "command editor unavailable")
		return
	}
	m := f.manager
	key, rec, err := m.scope(r)
	if err != nil || auth.OriginalCredentialFromContext(ctx) == nil {
		replyError(w, 403, "command access denied")
		return
	}
	if !m.pages.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	expected := m.config.PublicOrigin
	if expected == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		expected = scheme + "://" + r.Host
	}
	if r.Header.Get("Origin") != expected {
		replyError(w, 403, "command origin denied")
		return
	}
	var editor *commandProductEditor
	if f.editorBundle != nil {
		digest, indexURL := f.editorBundle(r)
		if !commandDigestValid("sha256:"+digest) || indexURL == "" {
			replyError(w, 403, "native command editor package unavailable")
			return
		}
		editor = &commandProductEditor{BundleDigest: digest, URL: indexURL}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, commandInputLimit+4096))
	if err != nil {
		replyError(w, 413, "command request too large")
		return
	}
	canonical, err := mooring.CanonicalJSON(raw)
	if err != nil {
		replyError(w, 400, "invalid command request")
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(canonical, &fields) != nil {
		replyError(w, 400, "invalid command request")
		return
	}
	for key, value := range fields {
		if (key != "action" && key != "view" && key != "create" && key != "write" && key != "raw") || bytes.Equal(value, []byte("null")) {
			replyError(w, 400, "invalid command request")
			return
		}
	}
	var input struct {
		Action string `json:"action"`
		View   string `json:"view,omitempty"`
		Create bool   `json:"create,omitempty"`
		Write  bool   `json:"write,omitempty"`
		Raw    string `json:"raw,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if len(canonical) == 0 || canonical[0] != '{' || decoder.Decode(&input) != nil {
		replyError(w, 400, "invalid command request")
		return
	}
	opening := input.Action == "open"
	if (!opening && input.Action != "read" && input.Action != "submit" && input.Action != "outcome") || (opening && (input.View != "" || input.Raw != "")) || (!opening && (input.Create || input.Write || input.View == "")) || ((input.Action == "read") && input.Raw != "") || ((input.Action == "submit" || input.Action == "outcome") && input.Raw == "") || len(input.Raw) > commandInputLimit {
		replyError(w, 400, "invalid command request")
		return
	}
	var document *commandProductDocument
	var view commandProductView
	if opening {
		document, err = f.Open(r, input.Create)
	} else {
		f.mu.Lock()
		view = f.views[input.View]
		stopped := f.closed
		f.mu.Unlock()
		document = view.document
		if stopped || document == nil || document.owner.Key != key {
			err = errCommandStore
		}
	}
	if err != nil || document == nil {
		replyError(w, 403, "command access unavailable")
		return
	}
	write := (opening && input.Write) || input.Action == "submit"
	if !opening && write && !view.original.write {
		replyError(w, 403, "command view is read only")
		return
	}
	original, err := document.store.Admit(ctx, write)
	if err != nil {
		replyError(w, 403, "command access denied")
		return
	}
	defer commandReleaseAdmission(document.store, original)
	if !opening && !commandSameView(view.original, original) {
		replyError(w, 403, "command view authority changed")
		return
	}
	if opening {
		input.View, err = collaborationOpaqueID()
		if err != nil {
			replyError(w, 503, "command view unavailable")
			return
		}
		f.mu.Lock()
		if f.closed || len(f.views) >= collaborationHTTPDocLimit*64 {
			err = errCommandStore
		} else {
			f.views[input.View] = commandProductView{document: document, original: original}
		}
		f.mu.Unlock()
		if err != nil {
			replyError(w, 503, "command view unavailable")
			return
		}
	}
	actor := commandEngineActorBinding{transportParticipant: original.participant, sourceUser: original.source.UserID, actor: "human:" + original.source.UserID}
	var outcome *commandSQLOutcome
	if input.Action == "submit" {
		value, e := document.store.Submit(r, original, actor, []byte(input.Raw))
		err = e
		if e == nil {
			outcome = &value
		}
	} else if input.Action == "outcome" {
		outcome, err = document.store.ReconcileOriginal(r, original, actor, []byte(input.Raw))
	}
	if err != nil && artifactStorageRefusal(w, err) {
		// The SQL transaction rolled back at the trigger: nothing was saved and
		// no outcome is pending, unlike the unconfirmed case below.
		return
	}
	if err != nil {
		replyError(w, 409, "command not confirmed; reconcile original outcome before further editing")
		return
	}
	started := false
	err = document.DeliverProjection(r, original, func(sendContext context.Context, projection []byte) error {
		var head struct {
			Revision uint64 `json:"revision"`
		}
		if json.Unmarshal(projection, &head) != nil || (outcome != nil && outcome.Revision != head.Revision) {
			return errCommandUnknown
		}
		// The send-time original check reserves only this physical I/O bound,
		// not the request's longer parsing/engine/SQL budget.
		sendDeadline, bounded := sendContext.Deadline()
		if !bounded || controller.SetWriteDeadline(sendDeadline) != nil {
			return errCommandStore
		}
		response, e := json.Marshal(commandProductHTTPResponse{input.View, actor.actor, commandFormat, commandEngineRevision, projection, outcome, editor})
		if e != nil {
			return e
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		started = true
		if _, e = w.Write(response); e != nil {
			return e
		}
		return controller.Flush()
	})
	// A failed physical send is an unknown delivery outcome. The operation is
	// never retried here, and a second error response must not follow partial JSON.
	if err != nil && !started {
		replyError(w, 409, "command projection not confirmed")
	}
}
