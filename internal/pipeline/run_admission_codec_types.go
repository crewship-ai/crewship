package pipeline

import "errors"

const runAdmissionNamespaceV1 = "crewship.routine-run-admission"
const runAdmissionFormatV1 = 1

var errRunAdmissionUnavailable = errors.New("routine run admission unavailable")

// An opaque codec value, never authentication or a runtime capability. Its
// private data is projected explicitly into the separate versioned envelope.
type runAdmissionV1 struct{ data runAdmissionDataV1 }

type runAdmissionWireV1 struct {
	Namespace string             `json:"namespace"`
	Format    int                `json:"format"`
	Data      runAdmissionDataV1 `json:"data"`
}

// All variable text is bytes, including identities and native TEXT values.
// Encoding/json therefore base64-encodes it without UTF-8 normalization.
type runAdmissionDataV1 struct {
	ID                      []byte                  `json:"id"`
	RunID                   []byte                  `json:"run_id"`
	Selection               runAdmissionSelectionV1 `json:"selection"`
	Bindings                runAdmissionBindingsV1  `json:"bindings"`
	Principal               runAdmissionPrincipalV1 `json:"principal"`
	Source                  runAdmissionSourceV1    `json:"source"`
	EffectiveDefinition     []byte                  `json:"effective_definition"`
	EffectiveDefinitionHash []byte                  `json:"effective_definition_hash"`
	ExecutedProjection      admissionNativeBytes    `json:"executed_projection"`
	AdmissionInputsJSON     []byte                  `json:"admission_inputs_json"`
	AdmissionMetadataJSON   []byte                  `json:"admission_metadata_json"`
	AdmissionEpoch          []byte                  `json:"admission_epoch"`
}

// Kind 0 is NULL, 1 is TEXT and 2 is BLOB. Present empty bytes are nonnil.
type admissionNativeBytes struct {
	Kind  uint8  `json:"kind"`
	Bytes []byte `json:"bytes"`
}

type runAdmissionSelectionV1 struct {
	Kind        uint8                  `json:"kind"`
	WorkspaceID []byte                 `json:"workspace_id"`
	PipelineID  []byte                 `json:"pipeline_id"`
	Archive     *runAdmissionArchiveV1 `json:"archive"`
	Head0       *runAdmissionHead0V1   `json:"head0"`
}

type runAdmissionArchiveV1 struct {
	VersionID            []byte               `json:"version_id"`
	Version              int64                `json:"version"`
	AuthoredDefinition   []byte               `json:"authored_definition"`
	DefinitionHash       []byte               `json:"definition_hash"`
	AuthorityFingerprint []byte               `json:"authority_fingerprint"`
	ArchivedOrigin       admissionNativeBytes `json:"archived_origin"`
}

type runAdmissionHead0V1 struct {
	CutoverReceiptID   []byte               `json:"cutover_receipt_id"`
	HeadVersion        int64                `json:"head_version"`
	AuthoredDefinition []byte               `json:"authored_definition"`
	DefinitionHash     []byte               `json:"definition_hash"`
	Crew               admissionNativeBytes `json:"crew"`
	DeclaredAgent      admissionNativeBytes `json:"declared_agent"`
	Tier               admissionNativeBytes `json:"tier"`
	Origin             admissionNativeBytes `json:"origin"`
}

type runAdmissionBindingsV1 struct {
	Source              uint8                `json:"source"`
	RootCrew            admissionNativeBytes `json:"root_crew"`
	DeclaredAuthorAgent admissionNativeBytes `json:"declared_author_agent"`
	Tier                admissionNativeBytes `json:"tier"`
	ActingAgent         admissionNativeBytes `json:"acting_agent"`
	StoredCeilingID     []byte               `json:"stored_ceiling_id"`
}

type runAdmissionPrincipalV1 struct {
	Kind             uint8                `json:"kind"`
	User             admissionNativeBytes `json:"user"`
	Crew             admissionNativeBytes `json:"crew"`
	Agent            admissionNativeBytes `json:"agent"`
	InvocationPolicy []byte               `json:"invocation_policy"`
}

type runAdmissionSourceV1 struct {
	TriggeredVia           admissionNativeBytes `json:"triggered_via"`
	TriggeredByID          admissionNativeBytes `json:"triggered_by_id"`
	InvocationOrigin       admissionNativeBytes `json:"invocation_origin"`
	AdmissionCurrentOrigin admissionNativeBytes `json:"admission_current_origin"`
	InvocationSourceRunID  admissionNativeBytes `json:"invocation_source_run_id"`
	ChainOrigin            admissionNativeBytes `json:"chain_origin"`
	ChainDepth             int64                `json:"chain_depth"`
	ParentAdmissionID      []byte               `json:"parent_admission_id"`
	ParentExecutionPath    []byte               `json:"parent_execution_path"`
	CallsiteStepID         []byte               `json:"callsite_step_id"`
}
