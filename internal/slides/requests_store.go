package slides

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	MaxSelectedActiveRequests      = 3
	MaxSelectedRequestsPerHour     = 30
	MaxSelectedRetainedRequests    = 2000
	MaxSelectedRequestHistoryBytes = 64 << 20
	MaxSelectedRequestEvents       = 8
	MaxSelectedRequestErrorBytes   = 1024
)

// Admission contains safe server-captured identities only, never bearer bytes.
// API authorization must revalidate the exact receipt/credential in its writer tx.
type SelectedAdmission struct {
	HumanUserID    string
	AgentID        string
	ChatID         string
	AudienceJSON   string
	CredentialKind string
	CredentialID   string
	CredentialHash string
}
type SelectedRequestSummary struct {
	ID                string        `json:"id"`
	PageID            string        `json:"page_id"`
	SchemaVersion     int           `json:"schema_version"`
	Version           int64         `json:"version"`
	Status            string        `json:"status"`
	BaseRevision      int64         `json:"base_revision"`
	BaseSchemaVersion int           `json:"base_schema_version"`
	BaseContentHash   string        `json:"base_content_hash"`
	Selection         TextSelection `json:"selection"`
	Instruction       string        `json:"instruction"`
	HumanActor        string        `json:"human_actor"`
	AgentActor        string        `json:"agent_actor"`
	OriginalRunID     string        `json:"original_run_id"`
	ResultRevision    *int64        `json:"result_revision,omitempty"`
	ResultHash        string        `json:"result_hash,omitempty"`
	Error             string        `json:"error"`
	Restored          bool          `json:"restored"`
	CreatedAt         string        `json:"created_at"`
	UpdatedAt         string        `json:"updated_at"`
}
type SelectedRequestEvent struct {
	Version    int64  `json:"version"`
	Kind       string `json:"kind"`
	Actor      string `json:"actor"`
	Detail     string `json:"detail"`
	CreatedAt  string `json:"created_at"`
	MutationID string `json:"mutation_id,omitempty"`
}
type SelectedRequestDetail struct {
	Request SelectedRequestSummary `json:"request"`
	History []SelectedRequestEvent `json:"history"`
}
type SelectedAdmissionReceipt struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
	Version   int64  `json:"version"`
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate"`
}

// SelectedAuthorityState is trusted host metadata for fenced authorization and
// dispatch. Never serialize this as a public response; use Summary/Detail DTOs.
type SelectedAuthorityState struct {
	Summary         SelectedRequestSummary
	Input           SelectedRequestInput
	Verified        VerifiedTextSelection
	Admission       SelectedAdmission
	OriginRequestID string
	RunID           string
	Sequence        int64
	Fingerprint     string
}

const selectedRequestColumns = `id,page_id,protocol_version,version,status,base_rev,base_schema_version,base_content_hash,input_json,verified_json,human_actor,agent_actor,COALESCE(human_user_id,''),COALESCE(agent_id,''),COALESCE(chat_id,''),audience_json,credential_kind,credential_id,credential_hash,origin_request_id,COALESCE(run_id,''),COALESCE(original_run_id,''),result_revision,COALESCE(result_hash,''),error,restored,created_at,updated_at,request_seq,fingerprint`

func scanSelectedRequest(row commentScanner) (SelectedAuthorityState, error) {
	var record SelectedAuthorityState
	var input, verified string
	var result sql.NullInt64
	r := &record.Summary
	a := &record.Admission
	err := row.Scan(&r.ID, &r.PageID, &r.SchemaVersion, &r.Version, &r.Status, &r.BaseRevision, &r.BaseSchemaVersion, &r.BaseContentHash, &input, &verified, &r.HumanActor, &r.AgentActor, &a.HumanUserID, &a.AgentID, &a.ChatID, &a.AudienceJSON, &a.CredentialKind, &a.CredentialID, &a.CredentialHash, &record.OriginRequestID, &record.RunID, &r.OriginalRunID, &result, &r.ResultHash, &r.Error, &r.Restored, &r.CreatedAt, &r.UpdatedAt, &record.Sequence, &record.Fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	if record.Input, err = decodeStoredSelectedInput([]byte(input)); err != nil {
		return record, errors.New("invalid stored selected request input")
	}
	if record.Verified, err = decodeVerifiedTextSelection([]byte(verified)); err != nil {
		return record, errors.New("invalid stored verified selection")
	}
	if result.Valid {
		revision := result.Int64
		r.ResultRevision = &revision
	}
	r.Selection = record.Input.Selection
	r.Instruction = record.Input.Instruction
	if r.SchemaVersion != SelectedRequestProtocolVersion || record.Input.PageID != r.PageID || record.Input.BaseRev != r.BaseRevision || record.Verified.DeckSchemaVersion != r.BaseSchemaVersion || record.Verified.ContentHash != r.BaseContentHash || record.Input.Selection != record.Verified.Selection || !identifier.MatchString(r.ID) || !identifier.MatchString(record.OriginRequestID) || (r.OriginalRunID != "" && !identifier.MatchString(r.OriginalRunID)) || r.Version < 1 || record.Sequence < 1 || !validText(r.Error, MaxSelectedRequestErrorBytes) {
		return record, errors.New("invalid stored selected request envelope")
	}
	return record, nil
}
func validateSelectedRecord(ctx context.Context, tx *sql.Tx, key Key, record SelectedAuthorityState) error {
	r := record.Summary
	if !identifier.MatchString(strings.TrimPrefix(r.HumanActor, "user:")) || !strings.HasPrefix(r.HumanActor, "user:") || !identifier.MatchString(strings.TrimPrefix(r.AgentActor, "agent:")) || !strings.HasPrefix(r.AgentActor, "agent:") {
		return errors.New("invalid retained selected request actors")
	}
	fingerprint, err := requestHash(key, "selected/admit", record.Input)
	if err != nil || fingerprint != record.Fingerprint {
		return errors.New("stored selected request fingerprint mismatch")
	}
	pinned, err := readState(ctx, tx, key, r.BaseRevision)
	if err != nil {
		return err
	}
	if err = ValidateVerifiedTextSelection(pinned, key.PageID, r.BaseRevision, record.Verified); err != nil {
		return errors.New("stored selected request differs from original pinned snapshot")
	}
	if selectedActive(r.Status) && (validateSelectedAdmission(record.Admission) != nil || r.HumanActor != "user:"+record.Admission.HumanUserID || r.AgentActor != "agent:"+record.Admission.AgentID || record.Input.ChatID != record.Admission.ChatID) {
		return errors.New("active selected request lost original admission")
	}
	if r.Restored && (selectedActive(r.Status) || record.RunID != "" || record.Admission.HumanUserID != "" || record.Admission.AgentID != "" || record.Admission.ChatID != "" || record.Admission.AudienceJSON != "{}" || record.Admission.CredentialID != "" || record.Admission.CredentialHash != "") {
		return errors.New("restored selected request is not inert")
	}
	if r.ResultRevision != nil {
		var parent int64
		var mutation, actor, kind, hash string
		err = tx.QueryRowContext(ctx, `SELECT parent_rev,mutation_id,actor,kind,content_hash FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=?`, key.WorkspaceID, key.PageID, *r.ResultRevision).Scan(&parent, &mutation, &actor, &kind, &hash)
		if err != nil || *r.ResultRevision != r.BaseRevision+1 || parent != r.BaseRevision || mutation != record.OriginRequestID+"-patch" || kind != "patch" || hash != r.ResultHash || actor != r.AgentActor+":"+r.OriginalRunID || r.OriginalRunID == "" {
			return errors.New("invalid retained request-owned result")
		}
	} else if r.ResultHash != "" || r.Status == "succeeded" {
		return errors.New("request success without owned result")
	}
	return nil
}

// ReadSelectedAuthorityTx must be called by trusted host authorization code in
// its existing writer fence. It validates immutable original/result bindings.
func ReadSelectedAuthorityTx(ctx context.Context, tx *sql.Tx, key Key, id string) (SelectedAuthorityState, error) {
	if !identifier.MatchString(id) {
		return SelectedAuthorityState{}, fmt.Errorf("%w: invalid request identity", ErrInvalid)
	}
	record, err := scanSelectedRequest(tx.QueryRowContext(ctx, `SELECT `+selectedRequestColumns+` FROM slides_requests WHERE workspace_id=? AND page_id=? AND id=?`, key.WorkspaceID, key.PageID, id))
	if err != nil {
		return record, err
	}
	return record, validateSelectedRecord(ctx, tx, key, record)
}
func selectedActive(status string) bool { return status == "queued" || status == "running" }
func validateSelectedAdmission(admission SelectedAdmission) error {
	if !identifier.MatchString(admission.HumanUserID) || !identifier.MatchString(admission.AgentID) || !identifier.MatchString(admission.ChatID) || !identifier.MatchString(admission.CredentialID) || len(admission.AudienceJSON) > 4096 || !json.Valid([]byte(admission.AudienceJSON)) || admission.AudienceJSON == "{}" {
		return errors.New("trusted selected admission required")
	}
	if admission.CredentialKind == "cli" && hashPattern.MatchString(admission.CredentialHash) {
		return nil
	}
	if admission.CredentialKind == "session" && admission.CredentialHash == "" {
		return nil
	}
	return errors.New("safe original credential identity required")
}
func selectedEvent(ctx context.Context, tx *sql.Tx, key Key, id string, version int64, kind, actor, detail, mutationID string) error {
	if version < 1 || version > MaxSelectedRequestEvents || actor == "" || len(actor) > 512 || !validText(detail, MaxSelectedRequestErrorBytes) {
		return errors.New("bounded selected event required")
	}
	var mutation any
	if mutationID != "" {
		if !identifier.MatchString(mutationID) {
			return errors.New("invalid selected event mutation identity")
		}
		mutation = mutationID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO slides_request_events(workspace_id,page_id,request_id,version,kind,actor,detail,mutation_id) VALUES(?,?,?,?,?,?,?,?)`, key.WorkspaceID, key.PageID, id, version, kind, actor, detail, mutation)
	return err
}
func (s *Store) AdmitSelectedRequest(ctx context.Context, key Key, id string, request SelectedRequestInput, admission SelectedAdmission, authorize Authorize) (result SelectedAdmissionReceipt, err error) {
	raw, err := canonicalValue(request)
	if err != nil {
		return result, err
	}
	if err = validateSelectedRequest(request); err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !identifier.MatchString(id) || validateSelectedAdmission(admission) != nil || admission.ChatID != request.ChatID {
		return result, fmt.Errorf("%w: trusted selected admission required", ErrInvalid)
	}
	fingerprint, err := requestHash(key, "selected/admit", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		if actor != "user:"+admission.HumanUserID {
			return errors.New("selected human actor mismatch")
		}
		prior, e := scanSelectedRequest(tx.QueryRowContext(ctx, `SELECT `+selectedRequestColumns+` FROM slides_requests WHERE workspace_id=? AND page_id=? AND human_actor=? AND submission_id=?`, key.WorkspaceID, key.PageID, actor, request.SubmissionID))
		if e == nil {
			if prior.Summary.Restored {
				return ErrDuplicate
			}
			if prior.Fingerprint != fingerprint || (selectedActive(prior.Summary.Status) && prior.Admission != admission) {
				return ErrConflict
			}
			if e = validateSelectedRecord(ctx, tx, key, prior); e != nil {
				return e
			}
			result = SelectedAdmissionReceipt{ID: prior.Summary.ID, RequestID: prior.Summary.ID, Version: prior.Summary.Version, Status: prior.Summary.Status, Duplicate: true}
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		current, e := readState(ctx, tx, key, 0)
		if e != nil {
			return e
		}
		verified, e := VerifyTextSelection(current, request)
		if e != nil {
			return e
		}
		verifiedJSON, e := canonicalValue(verified)
		if e != nil {
			return e
		}
		var active, recent, total, historyBytes int64
		e = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(status IN ('queued','running')),0),COALESCE(SUM(created_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 hour')),0),COUNT(*),COALESCE(SUM(length(CAST(input_json AS BLOB))+length(CAST(verified_json AS BLOB))+length(CAST(audience_json AS BLOB))),0) FROM slides_requests WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&active, &recent, &total, &historyBytes)
		if e != nil {
			return e
		}
		if active >= MaxSelectedActiveRequests || recent >= MaxSelectedRequestsPerHour || total >= MaxSelectedRetainedRequests || historyBytes+int64(len(raw)+len(verifiedJSON)+len(admission.AudienceJSON)) > MaxSelectedRequestHistoryBytes {
			return ErrQuota
		}
		var sequence int64
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(request_seq),0)+1 FROM slides_requests WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&sequence); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO slides_requests(id,workspace_id,page_id,request_seq,protocol_version,origin_request_id,human_actor,agent_actor,human_user_id,chat_id,agent_id,base_rev,base_schema_version,base_content_hash,input_json,verified_json,submission_id,fingerprint,audience_json,credential_kind,credential_id,credential_hash,status,version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'queued',1)`, id, key.WorkspaceID, key.PageID, sequence, SelectedRequestProtocolVersion, id, actor, "agent:"+admission.AgentID, admission.HumanUserID, admission.ChatID, admission.AgentID, request.BaseRev, verified.DeckSchemaVersion, verified.ContentHash, string(raw), string(verifiedJSON), request.SubmissionID, fingerprint, admission.AudienceJSON, admission.CredentialKind, admission.CredentialID, admission.CredentialHash)
		if e != nil {
			return e
		}
		if e = selectedEvent(ctx, tx, key, id, 1, "queued", actor, "", ""); e != nil {
			return e
		}
		result = SelectedAdmissionReceipt{ID: id, RequestID: id, Version: 1, Status: "queued"}
		return nil
	})
	return
}
