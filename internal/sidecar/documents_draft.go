package sidecar

// Draft packets retain the original preconditions. Applying a packet never reads
// a newer view or silently changes an uncertain mutation's identity.
import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

type DocumentDraftTarget struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Parent *string `json:"parent"`
	Text   string  `json:"text"`
	Hash   string  `json:"hash"`
}
type DocumentDraftView struct {
	PageID     string                `json:"page_id"`
	Schema     int                   `json:"schema"`
	Rev        int64                 `json:"rev"`
	Generation int64                 `json:"generation"`
	Epoch      string                `json:"epoch"`
	Targets    []DocumentDraftTarget `json:"targets"`
}
type DocumentDraftPatch struct {
	ID     string `json:"id"`
	Schema int    `json:"schema"`
	PageID string `json:"page_id"`
	Epoch  string `json:"epoch"`
	IfRev  int64  `json:"ifRev"`
	Target string `json:"target"`
	IfHash string `json:"ifHash"`
	Text   string `json:"text"`
}
type DocumentDraftCheckpoint struct {
	ID     string `json:"id"`
	Schema int    `json:"schema"`
	PageID string `json:"page_id"`
	Epoch  string `json:"epoch"`
	IfRev  int64  `json:"ifRev"`
}
type DocumentDraftPacket struct {
	Generation int64                   `json:"generation"`
	Patch      DocumentDraftPatch      `json:"patch"`
	Checkpoint DocumentDraftCheckpoint `json:"checkpoint"`
}
type DocumentDraftPrepare struct {
	View       DocumentDraftView `json:"view"`
	Target     string            `json:"target"`
	Text       string            `json:"text"`
	MutationID string            `json:"mutation_id"`
}
type DocumentDraftOutcome struct {
	Status     string          `json:"status"`
	Patch      json.RawMessage `json:"patch,omitempty"`
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	Error      string          `json:"error,omitempty"`
}

var draftID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var draftHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Reject duplicate keys at every depth as well as unknown struct fields. A
// duplicate must never make the model and server interpret different packets.
func DecodeDocumentDraft(raw []byte, out any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || len(raw) > 1<<20 || !utf8.Valid(raw) {
		return errors.New("draft JSON must fit 1 MiB of valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errors.New("duplicate draft field")
				}
				seen[key] = true
				if err = walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return errors.New("one closed draft object required")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing draft JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid or unknown draft field")
	}
	return nil
}
func requiredDraftFields(raw []byte, fields ...string) error {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return errors.New("draft object required")
	}
	for _, f := range fields {
		v, ok := obj[f]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errors.New("required draft field: " + f)
		}
	}
	return nil
}
func PrepareDocumentDraft(raw []byte) (DocumentDraftPacket, error) {
	var in DocumentDraftPrepare
	if err := DecodeDocumentDraft(raw, &in); err != nil {
		return DocumentDraftPacket{}, err
	}
	if err := requiredDraftFields(raw, "view", "target", "text", "mutation_id"); err != nil {
		return DocumentDraftPacket{}, err
	}
	var outer map[string]json.RawMessage
	_ = json.Unmarshal(raw, &outer)
	if len(outer["view"]) > 64<<10 {
		return DocumentDraftPacket{}, errors.New("draft view exceeds 64 KiB")
	}
	if err := requiredDraftFields(outer["view"], "page_id", "schema", "rev", "generation", "epoch", "targets"); err != nil {
		return DocumentDraftPacket{}, err
	}
	v := in.View
	if v.Schema != 3 || v.Rev < 0 || v.Rev == math.MaxInt64 || v.Generation < 0 || !draftID.MatchString(v.PageID) || v.Epoch == "" || len(v.Epoch) > 2048 || !draftID.MatchString(in.MutationID) || !draftID.MatchString(in.MutationID+"-checkpoint") || !draftID.MatchString(in.Target) {
		return DocumentDraftPacket{}, errors.New("invalid original draft view or mutation identity")
	}
	seen := map[string]bool{}
	hash := ""
	for _, t := range v.Targets {
		if seen[t.ID] || !draftID.MatchString(t.ID) || !draftHash.MatchString(t.Hash) || (t.Type != "paragraph" && t.Type != "heading") {
			return DocumentDraftPacket{}, errors.New("unsupported or ambiguous draft target")
		}
		seen[t.ID] = true
		if t.ID == in.Target {
			hash = t.Hash
		}
	}
	if hash == "" {
		return DocumentDraftPacket{}, errors.New("target absent from permitted draft view")
	}
	p := DocumentDraftPacket{Generation: v.Generation, Patch: DocumentDraftPatch{ID: in.MutationID, Schema: v.Schema, PageID: v.PageID, Epoch: v.Epoch, IfRev: v.Rev, Target: in.Target, IfHash: hash, Text: in.Text}, Checkpoint: DocumentDraftCheckpoint{ID: in.MutationID + "-checkpoint", Schema: v.Schema, PageID: v.PageID, Epoch: v.Epoch, IfRev: v.Rev + 1}}
	rawPacket, _ := json.Marshal(p)
	if len(rawPacket)+64 > 64<<10 {
		return DocumentDraftPacket{}, errors.New("prepared packet exceeds 64 KiB response limit")
	}
	return p, nil
}
func ParseDocumentDraftPacket(raw []byte) (DocumentDraftPacket, error) {
	var fields map[string]json.RawMessage
	if err := DecodeDocumentDraft(raw, &fields); err != nil {
		return DocumentDraftPacket{}, err
	}
	if _, wrapped := fields["packet"]; wrapped {
		var prepared struct {
			Status string          `json:"status"`
			Packet json.RawMessage `json:"packet"`
		}
		if err := DecodeDocumentDraft(raw, &prepared); err != nil || prepared.Status != "prepared_unsaved" {
			return DocumentDraftPacket{}, errors.New("invalid prepared draft wrapper")
		}
		raw = prepared.Packet
	}
	var p DocumentDraftPacket
	if err := DecodeDocumentDraft(raw, &p); err != nil {
		return p, err
	}
	if err := requiredDraftFields(raw, "generation", "patch", "checkpoint"); err != nil {
		return p, err
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	if err := requiredDraftFields(obj["patch"], "id", "schema", "page_id", "epoch", "ifRev", "target", "ifHash", "text"); err != nil {
		return p, err
	}
	if err := requiredDraftFields(obj["checkpoint"], "id", "schema", "page_id", "epoch", "ifRev"); err != nil {
		return p, err
	}
	a, b := p.Patch, p.Checkpoint
	if p.Generation < 0 || a.Schema != 3 || b.Schema != a.Schema || !draftID.MatchString(a.ID) || !draftID.MatchString(a.PageID) || !draftID.MatchString(a.Target) || !draftHash.MatchString(a.IfHash) || a.Epoch == "" || len(a.Epoch) > 2048 || a.IfRev < 0 || a.IfRev == math.MaxInt64 || !draftID.MatchString(b.ID) || b.ID != a.ID+"-checkpoint" || b.PageID != a.PageID || b.Epoch != a.Epoch || b.IfRev != a.IfRev+1 {
		return p, errors.New("inconsistent immutable draft packet")
	}
	return p, nil
}

// DocumentDraftExchange exchanges one bounded protected request. It must not
// retry, redirect, reread or substitute a human credential.
type DocumentDraftExchange func(string, []byte) (int, []byte, error)

func ApplyDocumentDraft(p DocumentDraftPacket, pinOnly bool, exchange DocumentDraftExchange) (DocumentDraftOutcome, error) {
	out := DocumentDraftOutcome{Status: "outcome_unknown"}
	rawPacket, _ := json.Marshal(p)
	if _, err := ParseDocumentDraftPacket(rawPacket); err != nil {
		out.Error = err.Error()
		return out, err
	}
	call := func(op string, request any) (json.RawMessage, error) {
		body, _ := json.Marshal(map[string]any{"operation": op, "generation": p.Generation, "request": request})
		status, raw, err := exchange(http.MethodPost, body)
		if err != nil {
			return nil, errors.New("draft outcome unknown; retain the exact packet for retry")
		}
		if len(raw) > 64<<10 || !json.Valid(raw) {
			return nil, errors.New("invalid bounded draft response; retain the exact packet")
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("draft operation HTTP%d denied or unavailable; retain the exact packet", status)
		}
		var receipt struct {
			ID         string `json:"id"`
			Checkpoint string `json:"checkpoint"`
			Rev        int64  `json:"rev"`
			Epoch      string `json:"epoch"`
		}
		if json.Unmarshal(raw, &receipt) != nil || receipt.Rev != p.Checkpoint.IfRev || receipt.Epoch != p.Patch.Epoch || (op == "patch" && receipt.ID != p.Patch.ID) || (op == "checkpoint" && (receipt.ID != p.Checkpoint.ID || receipt.Checkpoint != p.Checkpoint.ID)) {
			return nil, errors.New("draft receipt does not confirm this packet")
		}
		return raw, nil
	}
	var err error
	if !pinOnly {
		out.Patch, err = call("patch", p.Patch)
		if err != nil {
			out.Error = err.Error()
			return out, err
		}
	}
	out.Status = "committed_checkpoint_pending"
	out.Checkpoint, err = call("checkpoint", p.Checkpoint)
	if err != nil {
		out.Error = err.Error()
		if pinOnly {
			out.Status = "outcome_unknown"
		}
		return out, err
	}
	out.Status = "checkpointed"
	return out, nil
}
func (s *Server) documentDraftIdentity(w http.ResponseWriter, r *http.Request) bool {
	token := bearerToken(r)
	if !strings.HasPrefix(token, "agtv2.") {
		http.Error(w, "live run capability required", 403)
		return false
	}
	if _, _, _, present, valid := s.identityForRunTokenCtx(r.Context(), token); !present || !valid {
		http.Error(w, "live run capability refused", 403)
		return false
	}
	return true
}
func (s *Server) handleDocumentDraft(w http.ResponseWriter, r *http.Request) {
	if !s.documentDraftIdentity(w, r) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "unsupported draft method", 405)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 0 {
		http.Error(w, "draft view has no caller-selected scope", 400)
		return
	}
	release, ok := acquireArtifactForward(w, r)
	if !ok {
		return
	}
	defer release()
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		http.Error(w, "draft request exceeds 1 MiB", 413)
		return
	}
	if r.Method == http.MethodGet && len(raw) != 0 {
		http.Error(w, "draft view has no request body", 400)
		return
	}
	response, err := s.boundedHostRequest(r, r.Method, "/api/v1/internal/documents/draft", raw, 64<<10)
	if err != nil {
		http.Error(w, "draft outcome unavailable; retain the exact packet", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}
