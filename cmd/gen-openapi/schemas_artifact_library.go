package main

// Library publication and consumer pins (#5189).
func artifactLibrarySchemaCatalog() (map[string]DomainSchema, map[string]any) {
	integer := func(min int, description string) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min, "description": description}
	}
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	paths := map[string]any{"type": "array", "maxItems": 200, "items": map[string]any{"type": "string"}, "description": "Dot-joined token paths, sorted; at most 200."}
	filePaths := map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string"}, "description": "Dependency file paths, sorted."}
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	pin := map[string]any{
		"library_page_id": str("Library Page id. The copy outlives the Library."),
		"library_version": integer(1, "Library version the copy was made from."),
		"library_name":    str("Library name when the copy was made."),
		"sha256":          map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "SHA-256 of the copied design-system.json bytes."},
		"copied_by":       str("Human who made or updated the copy."),
		"copied_at":       map[string]any{"type": "string", "format": "date-time"},
		"tokens":          map[string]any{"type": "object", "additionalProperties": true, "description": "The pinned design-system.json, byte for byte."},
	}
	fileProps := map[string]any{
		"path":       str("Path of the file in the Library."),
		"media_type": map[string]any{"type": "string", "enum": []string{"font/woff2", "font/woff", "font/ttf", "font/otf", "text/plain"}},
		"status":     map[string]any{"type": "string", "enum": []string{"copied", "licence", "too_large"}, "description": "copied; licence (no redistributable licence, not copied); too_large (over 1 MiB, or over 4 MiB for the pin, not copied)."},
		"size":       integer(0, "Copied bytes; 0 when not copied."),
		"sha256":     map[string]any{"type": "string", "pattern": "^([0-9a-f]{64})?$", "description": "SHA-256 of the copied bytes; empty when not copied."},
		"family":     str("Font family from fonts.json; empty for an undeclared font or a licence file."),
		"weight":     integer(0, "Font weight from fonts.json (100-900)."),
		"style":      map[string]any{"type": "string", "enum": []string{"normal", "italic"}},
		"licence":    str("SPDX id of the licence that let the font travel."),
	}
	pin["files"] = map[string]any{"type": "array", "maxItems": 32, "items": object(fileProps, "path", "media_type", "status", "size", "sha256"), "description": "Dependency files the pin carries or left behind, without bytes."}
	withBytes := map[string]any{"content": str("Copied bytes in base64; empty when not copied.")}
	for k, v := range fileProps {
		withBytes[k] = v
	}
	withChanged := map[string]any{"changed": map[string]any{"type": "boolean", "description": "False when this version was already pinned (a safe retry)."}}
	for k, v := range pin {
		withChanged[k] = v
	}
	components := map[string]any{
		"ArtifactLibraryPublication": object(map[string]any{
			"published_version": integer(0, "Published version; 0 while nothing is published."),
			"published_by":      str("Owner who published it."),
			"published_at":      map[string]any{"type": "string", "format": "date-time"},
			"can_publish":       map[string]any{"type": "boolean", "description": "True only for the Library's human owner."},
		}, "published_version", "can_publish"),
		"ArtifactLibraryPublishRequest": object(map[string]any{"version": integer(1, "Retained version carrying design-system.json (a JSON object of at most 32 KiB).")}, "version"),
		"ArtifactLibraryPin":            object(pin, "library_page_id", "library_version", "library_name", "sha256", "copied_by", "copied_at", "tokens", "files"),
		"ArtifactLibraryPinResult":      object(withChanged, "library_page_id", "library_version", "library_name", "sha256", "copied_by", "copied_at", "tokens", "files", "changed"),
		"ArtifactLibraryPinFiles":       object(map[string]any{"files": map[string]any{"type": "array", "maxItems": 32, "items": object(withBytes, "path", "media_type", "status", "size", "sha256", "content")}}, "files"),
		"ArtifactLibraryPinRequest": object(map[string]any{
			"library":           map[string]any{"type": "string", "minLength": 1, "description": "Library slug, or @<page id>, in this workspace."},
			"version":           integer(1, "Published Library version the caller reviewed; a different publication returns 409 artifact_version_conflict."),
			"if_pinned_library": map[string]any{"type": "string", "description": "Library currently pinned as reviewed (slug or @<page id>); required exactly when if_pinned_version is not 0. A slug that no longer resolves returns 409 artifact_version_conflict; @<page id> always resolves."},
			"if_pinned_version": integer(0, "Library version currently pinned as reviewed, 0 when nothing is pinned; a mismatch returns 409 artifact_version_conflict."),
		}, "library", "version", "if_pinned_version"),
		"ArtifactLibraryUpdate": object(map[string]any{
			"pinned_version":    integer(1, "Pinned Library version."),
			"published_version": integer(0, "Library's published version; 0 when unpublished or not readable by the caller."),
			"library_available": map[string]any{"type": "boolean", "description": "False when the caller can no longer read the published version; nothing else about the Library is reported."},
			"update_available":  map[string]any{"type": "boolean", "description": "True when the published version differs from the pinned one."},
			"changes":           object(map[string]any{"added": paths, "removed": paths, "changed": paths, "truncated": map[string]any{"type": "boolean"}}, "added", "removed", "changed"),
			"file_changes":      object(map[string]any{"added": filePaths, "removed": filePaths, "changed": filePaths, "truncated": map[string]any{"type": "boolean"}}, "added", "removed", "changed"),
			"not_copied":        map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string"}, "description": "Fonts of the published version an update would leave behind, as path:status (licence or too_large)."},
		}, "pinned_version", "published_version", "library_available", "update_available", "changes", "file_changes", "not_copied"),
	}
	routes := map[string]DomainSchema{}
	add := func(method, suffix, response, request string, errors ...string) {
		definition := DomainSchema{Response: ref(response), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: errors}
		if request == "" {
			definition.NoRequest = true
		} else {
			definition.Request, definition.RequestRequired = ref(request), true
		}
		routes[method+" /api/v1/pages/{slug}/artifact"+suffix] = definition
	}
	add("GET", "/library", "ArtifactLibraryPublication", "", "400", "401", "403", "404", "500", "503")
	add("POST", "/library/publish", "ArtifactLibraryPublication", "ArtifactLibraryPublishRequest", "400", "401", "403", "404", "413", "500", "503")
	add("GET", "/library-pin", "ArtifactLibraryPin", "", "400", "401", "403", "404", "409", "500", "503")
	add("PUT", "/library-pin", "ArtifactLibraryPinResult", "ArtifactLibraryPinRequest", "400", "401", "403", "404", "409", "413", "500", "503")
	add("GET", "/library-pin/update", "ArtifactLibraryUpdate", "", "400", "401", "403", "404", "409", "500", "503")
	add("GET", "/library-pin/files", "ArtifactLibraryPinFiles", "", "400", "401", "403", "404", "409", "500", "503")
	return routes, components
}
