package work

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ErrSessionMessageConflict means an already accepted intent key names different
// original request input. The human HTTP boundary may map it to 409 after its
// live visibility check; this package exposes no HTTP surface.
var ErrSessionMessageConflict = errors.New("work: session message request changed")
var errSessionMessageInput = errors.New("work: invalid session message admission")

// SessionMessageInput is the complete original caller request. Explicit choices
// belong here; recaptured authority, resolved defaults and receipt times do not.
// The host validates the domain-specific intent and attachment schemas.
type SessionMessageInput struct {
	Body            string          `json:"body"`
	AttachmentsJSON json.RawMessage `json:"attachments"`
	IntentJSON      json.RawMessage `json:"intent"`
	TargetAgentID   string          `json:"target_agent_id"`
	Model           string          `json:"model"`
	Effort          string          `json:"effort"`
}

// SessionMessageRequest is host-only: scope and captured execution context must
// come from the authenticated server boundary, never decoded as a public grant.
// AgentID/CrewID and AuthorityJSON are captured once, not refreshed on retry.
type SessionMessageRequest struct {
	WorkspaceID   string              `json:"workspace_id"`
	UserID        string              `json:"user_id"`
	SessionID     string              `json:"session_id"`
	IntentKey     string              `json:"intent_key"`
	Input         SessionMessageInput `json:"input"`
	AgentID       string              `json:"agent_id"`
	CrewID        string              `json:"crew_id"`
	AuthorityJSON json.RawMessage     `json:"authority"`
}

type SessionMessageReceipt struct {
	MessageID  string
	WorkID     string
	Seq        int64
	AcceptedAt time.Time
	Duplicate  bool
}

// SessionMessageAdmission gives the mandatory host hook the ORIGINAL context on
// replay. Duplicate is a check-only phase: verify current visibility against that
// original proof, without issuing a new grant or refreshing captured defaults.
type SessionMessageAdmission struct {
	SessionMessageRequest
	MessageID  string    `json:"message_id"`
	AcceptedAt time.Time `json:"accepted_at"`
	Duplicate  bool      `json:"-"`
}

// The ledger digest protects this entire envelope. RequestFingerprint separately
// identifies the original caller input, excluding captured authority/defaults.
type sessionMessageEnvelope struct {
	SessionMessageAdmission
	RequestFingerprint string `json:"request_sha256"`
	Seq                int64  `json:"seq"`
}

// SessionMessageHook must do SQL only through tx. It must validate current human,
// workspace, chat and domain authority and transactionally check/apply the
// existing private-chat seal BEFORE returning. It owns policy and audit logic;
// this ledger deliberately does not duplicate that logic. A nil hook fails closed.
// The caller MUST roll back its transaction on every admission error.
type SessionMessageHook func(context.Context, *sql.Tx, SessionMessageAdmission) error

// AcceptSessionMessageTx atomically records a mailbox entry and SourceChat work
// in the existing tables. Use the existing immediate acceptance transaction; no
// dispatch or external side effects occur here. Only a successful commit permits
// returning the receipt. Retrying after a lost ACK returns the original receipt.
func (s *Store) AcceptSessionMessageTx(ctx context.Context, tx *sql.Tx, req SessionMessageRequest, hook SessionMessageHook) (SessionMessageReceipt, error) {
	if tx == nil || hook == nil || strings.TrimSpace(req.WorkspaceID) == "" || strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.IntentKey) == "" || strings.TrimSpace(req.Input.Body) == "" {
		return SessionMessageReceipt{}, errSessionMessageInput
	}
	var err error
	req.Input.AttachmentsJSON, err = canonicalMailboxJSON(req.Input.AttachmentsJSON, '[')
	if err != nil {
		return SessionMessageReceipt{}, err
	}
	req.Input.IntentJSON, err = canonicalMailboxJSON(req.Input.IntentJSON, '{')
	if err != nil {
		return SessionMessageReceipt{}, err
	}
	fingerprint := sessionMessageRequestDigest(req)
	scopeBytes, _ := json.Marshal([]string{req.WorkspaceID, req.UserID, req.SessionID, req.IntentKey})
	key := mailboxDigest(scopeBytes)
	var ack SessionMessageReceipt
	var accepted, storedInput, storedDigest, mailboxBody, mailboxAttachments string
	var bound struct{ workspace, session, human, source, ref, domainKind, domainID, agent, crew, class string }
	err = tx.QueryRowContext(ctx, `SELECT m.id,m.work_id,m.seq,m.created_at,w.input_json,w.input_sha256,m.body,m.attachments_json,w.workspace_id,w.session_id,w.authorized_by_user_id,w.source,w.source_ref,w.domain_kind,w.domain_id,w.agent_id,w.crew_id,w.class FROM session_mailbox m LEFT JOIN work_items w ON w.id=m.work_id WHERE m.workspace_id=? AND m.user_id=? AND m.session_id=? AND m.client_message_id=?`, req.WorkspaceID, req.UserID, req.SessionID, key).Scan(&ack.MessageID, &ack.WorkID, &ack.Seq, &accepted, &storedInput, &storedDigest, &mailboxBody, &mailboxAttachments, &bound.workspace, &bound.session, &bound.human, &bound.source, &bound.ref, &bound.domainKind, &bound.domainID, &bound.agent, &bound.crew, &bound.class)
	if err == nil {
		// Never hand unverified captured proof to policy or compare client input.
		if mailboxDigest([]byte(storedInput)) != storedDigest {
			return SessionMessageReceipt{}, errSessionMessageInput
		}
		var original sessionMessageEnvelope
		decoder := json.NewDecoder(strings.NewReader(storedInput))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&original) != nil || decoder.Decode(new(any)) != io.EOF || original.WorkspaceID != req.WorkspaceID || original.UserID != req.UserID || original.SessionID != req.SessionID || original.IntentKey != req.IntentKey || original.MessageID != ack.MessageID {
			return SessionMessageReceipt{}, errSessionMessageInput
		}
		ack.AcceptedAt, err = time.Parse(time.RFC3339Nano, accepted)
		if err != nil || !ack.AcceptedAt.Equal(original.AcceptedAt) || ack.Seq != original.Seq || original.RequestFingerprint != sessionMessageRequestDigest(original.SessionMessageRequest) {
			return SessionMessageReceipt{}, errSessionMessageInput
		}
		if bound.workspace != original.WorkspaceID || bound.session != original.SessionID || bound.human != original.UserID || bound.source != string(SourceChat) || bound.ref != ack.MessageID || bound.domainKind != "session_mailbox" || bound.domainID != ack.MessageID || bound.agent != original.AgentID || bound.crew != original.CrewID || bound.class != string(ClassChat) || mailboxBody != original.Input.Body || mailboxAttachments != string(original.Input.AttachmentsJSON) {
			return SessionMessageReceipt{}, errSessionMessageInput
		}
		original.Duplicate = true
		// Visibility must win over conflict: a revoked caller learns no comparison.
		if err = hook(ctx, tx, original.SessionMessageAdmission); err != nil {
			return SessionMessageReceipt{}, err
		}
		if original.RequestFingerprint != fingerprint {
			return SessionMessageReceipt{}, ErrSessionMessageConflict
		}
		ack.Duplicate = true
		return ack, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SessionMessageReceipt{}, err
	}
	if strings.TrimSpace(req.AgentID) == "" {
		return SessionMessageReceipt{}, errSessionMessageInput
	}
	req.AuthorityJSON, err = canonicalMailboxJSON(req.AuthorityJSON, '{')
	if err != nil {
		return SessionMessageReceipt{}, err
	}
	ack.MessageID = s.idFn()
	ack.AcceptedAt = s.now().UTC()
	// Session IDs are globally stable; preserve the existing session-wide sequence.
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(seq),0)+1 FROM session_mailbox WHERE session_id=?`, req.SessionID).Scan(&ack.Seq); err != nil {
		return SessionMessageReceipt{}, err
	}
	admission := SessionMessageAdmission{SessionMessageRequest: req, MessageID: ack.MessageID, AcceptedAt: ack.AcceptedAt}
	// Freeze the durable envelope before handing copies of its JSON to host policy.
	envelope, err := json.Marshal(sessionMessageEnvelope{SessionMessageAdmission: admission, RequestFingerprint: fingerprint, Seq: ack.Seq})
	if err != nil {
		return SessionMessageReceipt{}, errSessionMessageInput
	}
	admission.Input.AttachmentsJSON = bytes.Clone(admission.Input.AttachmentsJSON)
	admission.Input.IntentJSON = bytes.Clone(admission.Input.IntentJSON)
	admission.AuthorityJSON = bytes.Clone(admission.AuthorityJSON)
	if err = hook(ctx, tx, admission); err != nil {
		return SessionMessageReceipt{}, err
	}
	if err = s.CheckIngressTx(ctx, tx, s.ingressLimits, IngressRequest{WorkspaceID: req.WorkspaceID, BodyBytes: int64(len(envelope))}); err != nil {
		return SessionMessageReceipt{}, err
	}
	workReceipt, err := s.AcceptTx(ctx, tx, AcceptRequest{WorkspaceID: req.WorkspaceID, Source: SourceChat, SourceRef: ack.MessageID, DomainKind: "session_mailbox", DomainID: ack.MessageID, AgentID: req.AgentID, CrewID: req.CrewID, SessionID: req.SessionID, Class: ClassChat, AuthorizedByUserID: req.UserID, InputJSON: string(envelope), InputSHA256: mailboxDigest(envelope)})
	if err != nil {
		return SessionMessageReceipt{}, err
	}
	ack.WorkID = workReceipt.WorkID
	_, err = tx.ExecContext(ctx, `INSERT INTO session_mailbox(id,workspace_id,user_id,session_id,client_message_id,seq,body,attachments_json,state,work_id,created_at) VALUES(?,?,?,?,?,?,?,?, 'pending',?,?)`, ack.MessageID, req.WorkspaceID, req.UserID, req.SessionID, key, ack.Seq, req.Input.Body, string(req.Input.AttachmentsJSON), ack.WorkID, tsformat.Format(ack.AcceptedAt))
	if err != nil {
		return SessionMessageReceipt{}, err
	}
	return ack, nil
}
func sessionMessageRequestDigest(req SessionMessageRequest) string {
	raw, _ := json.Marshal(struct {
		Workspace string              `json:"workspace"`
		Human     string              `json:"human"`
		Chat      string              `json:"chat"`
		Key       string              `json:"key"`
		Input     SessionMessageInput `json:"input"`
	}{req.WorkspaceID, req.UserID, req.SessionID, req.IntentKey, req.Input})
	return mailboxDigest(raw)
}

func mailboxDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// The parser rejects duplicate keys at every level, trailing input and excessive
// JSON nesting using encoding/json's existing validity limit. Its byte ceiling is
// the existing workspace ingress budget, not a new admission profile.
func canonicalMailboxJSON(raw []byte, kind byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || int64(len(trimmed)) > DefaultWorkspaceRawBytes || trimmed[0] != kind || !json.Valid(trimmed) {
		return nil, errSessionMessageInput
	}
	d := json.NewDecoder(bytes.NewReader(trimmed))
	d.UseNumber()
	value, err := mailboxJSONValue(d)
	if err != nil {
		return nil, errSessionMessageInput
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errSessionMessageInput
	}
	result, err := json.Marshal(value)
	if err != nil {
		return nil, errSessionMessageInput
	}
	return result, nil
}
func mailboxJSONValue(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		value := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, errSessionMessageInput
			}
			if _, exists := value[key]; exists {
				return nil, errSessionMessageInput
			}
			v, err := mailboxJSONValue(d)
			if err != nil {
				return nil, err
			}
			value[key] = v
		}
		_, err = d.Token()
		return value, err
	case json.Delim('['):
		value := []any{}
		for d.More() {
			v, err := mailboxJSONValue(d)
			if err != nil {
				return nil, err
			}
			value = append(value, v)
		}
		_, err = d.Token()
		return value, err
	default:
		return token, nil
	}
}
