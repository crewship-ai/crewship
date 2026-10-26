package api

import (
	"crypto/sha256"
	"encoding/hex"
)

// Correlate only host-issued opaque capabilities, never an identity, credential,
// document frame or error string. The reference is not an admission capability.
func collaborationAdmissionRef(admission collaborationHumanAdmission) string {
	sum := sha256.Sum256([]byte(admission.participant))
	return hex.EncodeToString(sum[:])
}

// Diagnostic only: retain the exact denial error and every original authority
// decision. Bound failures across the registry, including repeated HTTP calls.
func (s *collaborationHumanRegistry) logAdmissionDenial(admission collaborationHumanAdmission, check string, err error) error {
	if err == nil || s == nil || s.pages == nil || s.pages.logger == nil {
		return err
	}
	switch check {
	case "session_store", "ingress_fence", "clock", "clock_epoch", "admission_expired",
		"write_capability", "author_binding", "author_vector_check", "author_vector_changed",
		"current_page_authority", "membership_binding", "final_clock_epoch",
		"final_admission_expired", "admission_remaining_horizon", "credential_remaining_horizon",
		"delivery_owner_horizon", "send_session_store", "send_gate_lookup", "send_gate_clock",
		"send_gate_closed", "send_gate_expired", "send_gate_horizon", "send_owner_lookup",
		"send_owner_horizon", "send_owner_check", "send_write_deadline", "send_transport":
	default:
		check = "unclassified"
	}
	s.mu.Lock()
	if s.denialLogs >= 32 {
		s.mu.Unlock()
		return err
	}
	s.denialLogs++
	s.mu.Unlock()
	s.pages.logger.Info("mooring admission denied", "admission_ref", collaborationAdmissionRef(admission), "failed_check", check)
	return err
}
