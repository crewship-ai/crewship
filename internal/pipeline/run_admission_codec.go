package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
)

func encodeRunAdmissionV1(value runAdmissionV1, maxWireBytes int) ([]byte, error) {
	if maxWireBytes <= 0 || !runAdmissionByteBudget(value.data, maxWireBytes) || !validRunAdmissionData(value.data) {
		return nil, errRunAdmissionUnavailable
	}
	wire := runAdmissionWireV1{Namespace: runAdmissionNamespaceV1, Format: runAdmissionFormatV1, Data: value.data}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > maxWireBytes {
		return nil, errRunAdmissionUnavailable
	}
	return raw, nil
}

func decodeRunAdmissionV1(raw []byte, maxWireBytes int) (runAdmissionV1, error) {
	if maxWireBytes <= 0 || len(raw) == 0 || len(raw) > maxWireBytes {
		return runAdmissionV1{}, errRunAdmissionUnavailable
	}
	var wire runAdmissionWireV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil || wire.Namespace != runAdmissionNamespaceV1 || wire.Format != runAdmissionFormatV1 || !validRunAdmissionData(wire.Data) {
		return runAdmissionV1{}, errRunAdmissionUnavailable
	}
	// Fixed canonical output also rejects duplicate fields, trailing values,
	// omitted fields, alternate base64 and numeric encodings and whitespace.
	canonical, err := json.Marshal(wire)
	if err != nil || !bytes.Equal(raw, canonical) {
		return runAdmissionV1{}, errRunAdmissionUnavailable
	}
	return runAdmissionV1{data: wire.Data}, nil
}

func validRunAdmissionData(d runAdmissionDataV1) bool {
	if len(d.ID) == 0 || len(d.RunID) == 0 || len(d.Selection.WorkspaceID) == 0 || len(d.Selection.PipelineID) == 0 || len(d.AdmissionEpoch) == 0 ||
		!runAdmissionHashMatches(d.EffectiveDefinition, d.EffectiveDefinitionHash) || d.AdmissionInputsJSON == nil || d.AdmissionMetadataJSON == nil || d.Source.ChainDepth < 0 {
		return false
	}
	// Capture shape only: native NULL or exact effective TEXT. Neither is authority.
	projection := d.ExecutedProjection
	if projection.Kind != 0 && projection.Kind != 1 || projection.Kind == 1 && !bytes.Equal(projection.Bytes, d.EffectiveDefinition) {
		return false
	}
	legacy := false
	switch d.Selection.Kind {
	case 1:
		a := d.Selection.Archive
		if a == nil || d.Selection.Head0 != nil || len(a.VersionID) == 0 || a.Version < 1 || !runAdmissionHashMatches(a.AuthoredDefinition, a.DefinitionHash) {
			return false
		}
		legacy = bytes.Equal(a.AuthorityFingerprint, []byte("legacy-trusted:v1"))
		if !legacy && !runAdmissionLowerHex64(a.AuthorityFingerprint) {
			return false
		}
	case 2:
		h := d.Selection.Head0
		if h == nil || d.Selection.Archive != nil || len(h.CutoverReceiptID) == 0 || h.HeadVersion != 0 || !runAdmissionHashMatches(h.AuthoredDefinition, h.DefinitionHash) {
			return false
		}
		legacy = true
	default:
		return false
	}
	if legacy {
		if d.Bindings.Source != 1 || d.Bindings.StoredCeilingID != nil {
			return false
		}
	} else if d.Bindings.Source != 2 || len(d.Bindings.StoredCeilingID) == 0 || len(d.Bindings.ActingAgent.Bytes) == 0 {
		return false
	}
	for _, native := range runAdmissionNativeValues(d) {
		if native.Kind == 0 {
			if native.Bytes != nil {
				return false
			}
		} else if (native.Kind != 1 && native.Kind != 2) || native.Bytes == nil {
			return false
		}
	}
	// This is structural presence, not an account/member/permission check.
	if d.Principal.Kind == 1 {
		if len(d.Principal.User.Bytes) == 0 {
			return false
		}
	} else if d.Principal.Kind != 2 || d.Principal.User.Kind != 0 || d.Principal.User.Bytes != nil {
		return false
	}
	parent := d.Source.ParentAdmissionID
	path := d.Source.ParentExecutionPath
	step := d.Source.CallsiteStepID
	return parent == nil && path == nil && step == nil || len(parent) > 0 && len(path) > 0 && len(step) > 0
}

func runAdmissionHashMatches(raw, hash []byte) bool {
	if len(raw) == 0 || len(hash) != sha256.Size {
		return false
	}
	actual := sha256.Sum256(raw)
	return bytes.Equal(hash, actual[:])
}

func runAdmissionLowerHex64(raw []byte) bool {
	if len(raw) != 64 {
		return false
	}
	for _, c := range raw {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func runAdmissionNativeValues(d runAdmissionDataV1) []admissionNativeBytes {
	out := []admissionNativeBytes{d.ExecutedProjection, d.Bindings.RootCrew, d.Bindings.DeclaredAuthorAgent, d.Bindings.Tier, d.Bindings.ActingAgent,
		d.Principal.User, d.Principal.Crew, d.Principal.Agent, d.Source.TriggeredVia, d.Source.TriggeredByID,
		d.Source.InvocationOrigin, d.Source.AdmissionCurrentOrigin, d.Source.InvocationSourceRunID, d.Source.ChainOrigin}
	if a := d.Selection.Archive; a != nil {
		out = append(out, a.ArchivedOrigin)
	}
	if h := d.Selection.Head0; h != nil {
		out = append(out, h.Crew, h.DeclaredAgent, h.Tier, h.Origin)
	}
	return out
}

// A pre-marshal raw-byte bound avoids encoding arbitrarily oversized fields.
// The final canonical wire length (including base64 and field names) is checked
// separately. The positive bound is host-owned, not a runtime product limit.
func runAdmissionByteBudget(d runAdmissionDataV1, remaining int) bool {
	parts := [][]byte{d.ID, d.RunID, d.Selection.WorkspaceID, d.Selection.PipelineID, d.AdmissionEpoch,
		d.EffectiveDefinition, d.EffectiveDefinitionHash, d.AdmissionInputsJSON, d.AdmissionMetadataJSON,
		d.Bindings.StoredCeilingID, d.Principal.InvocationPolicy, d.Source.ParentAdmissionID, d.Source.ParentExecutionPath, d.Source.CallsiteStepID}
	if a := d.Selection.Archive; a != nil {
		parts = append(parts, a.VersionID, a.AuthoredDefinition, a.DefinitionHash, a.AuthorityFingerprint)
	}
	if h := d.Selection.Head0; h != nil {
		parts = append(parts, h.CutoverReceiptID, h.AuthoredDefinition, h.DefinitionHash)
	}
	for _, n := range runAdmissionNativeValues(d) {
		parts = append(parts, n.Bytes)
	}
	for _, raw := range parts {
		if len(raw) > remaining {
			return false
		}
		remaining -= len(raw)
	}
	return true
}
