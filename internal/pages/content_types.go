package pages

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

// ContentTypeDescriptor names compiled adapters, never document-supplied code.
// It describes a format, not permissions or runtime availability; existing
// authorization fences still control every adapter operation.
type ContentTypeDescriptor struct {
	TypeID            string   `json:"type_id"`
	SchemaVersion     int      `json:"schema_version"`
	Viewer            string   `json:"viewer"`
	Editor            string   `json:"editor"`
	StorageAdapter    string   `json:"storage_adapter"`
	ProjectionAdapter string   `json:"projection_adapter"`
	AnchorKinds       []string `json:"anchor_kinds"`
	ExportFormats     []string `json:"export_formats"`
	ExecutionProfile  string   `json:"execution_profile"`
	AuthoringSkill    string   `json:"authoring_skill"`
}

//go:embed content_types.json
var contentTypeData []byte

var contentTypeRegistry = func() []ContentTypeDescriptor {
	var descriptors []ContentTypeDescriptor
	decoder := json.NewDecoder(bytes.NewReader(contentTypeData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptors); err != nil {
		panic(fmt.Errorf("compiled Page content registry: %w", err))
	}
	seen := map[struct {
		id      string
		version int
	}]bool{}
	for _, descriptor := range descriptors {
		if descriptor.TypeID == "" || descriptor.SchemaVersion < 1 || seen[struct {
			id      string
			version int
		}{descriptor.TypeID, descriptor.SchemaVersion}] || descriptor.Viewer == "" || descriptor.Editor == "" || descriptor.StorageAdapter == "" || descriptor.ProjectionAdapter == "" || descriptor.ExecutionProfile == "" || descriptor.AnchorKinds == nil || descriptor.ExportFormats == nil {
			panic("invalid compiled Page content descriptor")
		}
		seen[struct {
			id      string
			version int
		}{descriptor.TypeID, descriptor.SchemaVersion}] = true
	}
	return descriptors
}()

func ContentTypes() []ContentTypeDescriptor {
	result := make([]ContentTypeDescriptor, len(contentTypeRegistry))
	for i, descriptor := range contentTypeRegistry {
		descriptor.AnchorKinds = append([]string{}, descriptor.AnchorKinds...)
		descriptor.ExportFormats = append([]string{}, descriptor.ExportFormats...)
		result[i] = descriptor
	}
	return result
}

func LookupContentType(id string, version int) (ContentTypeDescriptor, bool) {
	for _, descriptor := range ContentTypes() {
		if descriptor.TypeID == id && descriptor.SchemaVersion == version {
			return descriptor, true
		}
	}
	return ContentTypeDescriptor{}, false
}

// ContentReference is derived metadata, not a writable discriminator or grant.
// Clients resolve it against their own compiled registry. A server can reject a
// stored profile even if its type/version otherwise exists in the registry.
type ContentReference struct {
	TypeID        string `json:"type_id"`
	SchemaVersion int    `json:"schema_version"`
	Supported     bool   `json:"supported"`
	Reason        string `json:"reason,omitempty"`
}

type ContentFacts struct {
	HasSlides       bool   `json:"has_slides"`
	SlidesSchema    int    `json:"slides_schema"`
	HasDocument     bool   `json:"has_document"`
	DocumentSchema  int    `json:"document_schema"`
	HasBoard        bool   `json:"has_board"`
	BoardFormat     string `json:"board_format"`
	HasArtifact     bool   `json:"has_artifact"`
	ArtifactProfile string `json:"artifact_profile"`
	ArtifactVersion int    `json:"artifact_version"`
	HasProject      bool   `json:"has_project"`
	HasApplication  bool   `json:"has_application"`
	HasPanels       bool   `json:"has_panels"`
	HumanOwned      bool   `json:"human_owned"`
}

type ContentDerivation struct {
	ContentDescriptor     ContentReference  `json:"content_descriptor"`
	ApplicationDescriptor *ContentReference `json:"application_descriptor,omitempty"`
	// Eligibility only; every create/write still needs the existing live ACL.
	CanInitializeContent bool `json:"can_initialize_content"`
}

func contentReference(id string, version int) ContentReference {
	_, supported := LookupContentType(id, version)
	reference := ContentReference{TypeID: id, SchemaVersion: version, Supported: supported}
	if !supported {
		reference.Reason = "unsupported-version"
	}
	return reference
}

func DeriveContentTypes(facts ContentFacts) ContentDerivation {
	result := ContentDerivation{ContentDescriptor: contentReference("panels", 1)}
	specialized := 0
	for _, present := range []bool{facts.HasSlides, facts.HasDocument, facts.HasBoard, facts.HasArtifact} {
		if present {
			specialized++
		}
	}
	if specialized > 1 || (specialized != 0 && (facts.HasPanels || facts.HasProject || facts.HasApplication)) {
		result.ContentDescriptor = ContentReference{TypeID: "conflicting", Supported: false, Reason: "conflicting-storage"}
		return result
	}
	switch {
	case facts.HasSlides:
		result.ContentDescriptor = ContentReference{TypeID: "slides", SchemaVersion: facts.SlidesSchema, Supported: false, Reason: "viewer-unavailable"}
	case facts.HasDocument:
		result.ContentDescriptor = contentReference("doc", facts.DocumentSchema)
	case facts.HasBoard:
		result.ContentDescriptor = contentReference("board", 1)
		if facts.BoardFormat != BoardFormat {
			result.ContentDescriptor.Supported = false
			result.ContentDescriptor.Reason = "unsupported-profile"
		}
	case facts.HasArtifact:
		result.ContentDescriptor = contentReference("html", facts.ArtifactVersion)
		if facts.ArtifactProfile != "static-html-v1" {
			result.ContentDescriptor.Supported = false
			result.ContentDescriptor.Reason = "unsupported-profile"
		}
	default:
		// Source existence and a live publication are independent. Panels
		// remain the primary surface; the optional application can coexist.
		if facts.HasProject || facts.HasApplication {
			application := contentReference("application", 1)
			result.ApplicationDescriptor = &application
		}
		result.CanInitializeContent = facts.HumanOwned && !facts.HasPanels && !facts.HasProject && !facts.HasApplication
	}
	return result
}
