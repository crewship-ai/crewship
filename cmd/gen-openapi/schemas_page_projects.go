package main

import "github.com/crewship-ai/crewship/internal/pages"

func pageProjectSourceSchema() map[string]any {
	file := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path", "encoding", "content"}, "properties": map[string]any{
		"path":     map[string]any{"type": "string", "maxLength": 240, "description": "Portable relative path; no traversal, duplicates, case collisions or reserved files."},
		"encoding": map[string]any{"type": "string", "enum": []string{"utf8", "base64"}},
		"content":  map[string]any{"type": "string", "description": "Exact UTF-8 text or canonical base64. At most 512 KiB decoded per file."},
	}}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"format", "runtime", "files"}, "properties": map[string]any{
		"format":  map[string]any{"type": "string", "enum": []string{pages.SourceProjectFormat}},
		"runtime": map[string]any{"type": "string", "enum": []string{pages.SourceProjectRuntime}},
		"files":   map[string]any{"type": "array", "minItems": 3, "maxItems": pages.MaxProjectFiles, "items": file, "description": "Requires package.json, pnpm-lock.yaml and index.html; 2 MiB total decoded source."},
	}}
}

func pageProjectSchemaCatalog() map[string]DomainSchema {
	source := pageProjectSourceSchema()
	digest := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	revision := map[string]any{"type": "integer", "minimum": 1}
	definition := map[string]any{"type": "object", "required": []string{"apiVersion", "kind", "metadata", "spec"}, "description": "Authored kind: Page document; draft-only, no publication authority."}
	job := map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "source_revision": revision, "source_digest": digest,
		"state":           map[string]any{"type": "string", "enum": []string{"running", "ready", "failed", "interrupted"}},
		"artifact_digest": digest, "error": map[string]any{"type": "string"}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "completed_at": map[string]any{"type": "string", "format": "date-time"},
	}}
	nullableJob := map[string]any{"type": "object", "nullable": true, "properties": job["properties"]}
	artifact := map[string]any{"type": "object", "required": []string{"format", "javascript", "css", "toolchain"}, "properties": map[string]any{
		"format":     map[string]any{"type": "string", "enum": []string{"crewship-page-preview/v1"}},
		"javascript": map[string]any{"type": "string"}, "css": map[string]any{"type": "string"}, "toolchain": map[string]any{"type": "string"}, "profile_sha256": digest,
	}}

	checkpoint := map[string]any{"type": "string", "pattern": "^([0-9a-f]{40})?$"}
	historical := map[string]any{"type": "object", "required": []string{"revision", "digest", "git_commit", "definition", "project"}, "properties": map[string]any{"revision": revision, "digest": digest, "git_commit": checkpoint, "definition": definition, "project": source}}
	historyItem := map[string]any{"type": "object", "properties": map[string]any{"revision": revision, "digest": digest, "git_commit": checkpoint, "actor": map[string]any{"type": "string"}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "restorable": map[string]any{"type": "boolean"}}}
	publication := map[string]any{"type": "object", "properties": map[string]any{"version": revision, "build_id": map[string]any{"type": "string"}, "source_revision": revision, "source_digest": digest, "git_commit": checkpoint, "artifact_digest": digest, "created_at": map[string]any{"type": "string", "format": "date-time"}}}
	publicationHistoryProperties := map[string]any{}
	for key, value := range publication["properties"].(map[string]any) {
		publicationHistoryProperties[key] = value
	}
	publicationHistoryProperties["actor"] = map[string]any{"type": "string"}
	publicationHistoryProperties["rollback_of"] = map[string]any{"type": "integer", "minimum": 0}
	publicationHistoryProperties["withdrawn_at"] = map[string]any{"type": "string"}
	publicationHistoryItem := map[string]any{"type": "object", "properties": publicationHistoryProperties}
	receiptProps := map[string]any{}
	for key, value := range publication["properties"].(map[string]any) {
		receiptProps[key] = value
	}
	for _, key := range []string{"published", "is_current", "replayed"} {
		receiptProps[key] = map[string]any{"type": "boolean"}
	}
	receiptProps["live_version"] = map[string]any{"type": "integer", "minimum": 0}
	receipt := map[string]any{"type": "object", "properties": receiptProps}
	return map[string]DomainSchema{
		"POST /api/v1/pages/maintenance":                                     {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"discard_history": map[string]any{"type": "boolean"}, "confirm": map[string]any{"type": "boolean"}}}, Response: map[string]any{"type": "object", "properties": map[string]any{"compacted": map[string]any{"type": "boolean"}, "discarded_optional_history": map[string]any{"type": "boolean"}, "preserved": map[string]any{"type": "string"}}}},
		"GET /api/v1/pages/{slug}/project/fsck":                              {Response: map[string]any{"type": "object", "properties": map[string]any{"healthy": map[string]any{"type": "boolean"}, "checked_sources": map[string]any{"type": "integer"}, "checked_git_objects": map[string]any{"type": "integer"}, "checked_checkpoints": map[string]any{"type": "integer"}, "checked_artifacts": map[string]any{"type": "integer"}, "failures": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"}}}}}}},
		"GET /api/v1/pages/{slug}/application/panels/{panelId}/history":      {Response: map[string]any{"type": "object", "properties": map[string]any{"publication": revision, "next_before": map[string]any{"type": "integer", "minimum": 0}, "items": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "properties": map[string]any{"sequence": revision, "data": map[string]any{}, "producedAt": map[string]any{"type": "string"}, "state": map[string]any{"type": "string"}}}}}}},
		"GET /api/v1/pages/{slug}/project/publications":                      {Response: map[string]any{"type": "object", "properties": map[string]any{"publications": map[string]any{"type": "array", "maxItems": 50, "items": publicationHistoryItem}, "publication_version": map[string]any{"type": "integer", "minimum": 0}, "published": map[string]any{"type": "boolean"}, "can_publish": map[string]any{"type": "boolean"}, "next_before": map[string]any{"type": "integer", "minimum": 0}}}},
		"POST /api/v1/pages/{slug}/project/unpublish":                        {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_publication"}, "properties": map[string]any{"expected_publication": revision}}, Response: map[string]any{"type": "object", "properties": map[string]any{"publication": map[string]any{"type": "object", "nullable": true}, "publication_version": revision, "published": map[string]any{"type": "boolean", "enum": []bool{false}}}}},
		"POST /api/v1/pages/{slug}/project/check":                            {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"build_id", "expected_revision"}, "properties": map[string]any{"build_id": map[string]any{"type": "string"}, "expected_revision": revision}}, Response: map[string]any{"type": "object", "properties": map[string]any{"build_id": map[string]any{"type": "string"}, "source_revision": revision, "git_commit": checkpoint, "artifact_digest": digest, "checks": map[string]any{"type": "object", "additionalProperties": true}}, "description": "Integrity, compiler and current binding checks. Browser and security review still required."}},
		"POST /api/v1/pages/{slug}/project/publish":                          {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_publication", "reviewed_code"}, "properties": map[string]any{"build_id": map[string]any{"type": "string"}, "expected_revision": revision, "expected_publication": map[string]any{"type": "integer", "minimum": 0}, "reviewed_code": map[string]any{"type": "boolean", "enum": []bool{true}}, "rollback_version": revision}}, Response: receipt},
		"GET /api/v1/pages/{slug}/application":                               {Response: map[string]any{"type": "object", "properties": map[string]any{"publication": map[string]any{"type": "object", "nullable": true, "properties": publication["properties"]}, "publication_version": map[string]any{"type": "integer", "minimum": 0}, "can_publish": map[string]any{"type": "boolean"}, "artifact": artifact, "runtime_url": map[string]any{"type": "string"}, "development_same_origin": map[string]any{"type": "boolean", "description": "Explicit operator setting for reviewed same-origin development demos; does not provide browser process isolation."}}}},
		"POST /api/v1/pages/{slug}/application/actions/{panelId}/{actionId}": {SuccessStatuses: []string{"202"}, RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"publication"}, "properties": map[string]any{"publication": revision, "inputs": map[string]any{"type": "object"}}}, Response: map[string]any{"type": "object", "required": []string{"pending_id", "status"}, "properties": map[string]any{"pending_id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"SCHEDULED", "DEDUPED"}}}}},
		"GET /api/v1/pages/{slug}/application/actions/{pendingId}":           {Response: map[string]any{"type": "object", "properties": map[string]any{"pending_id": map[string]any{"type": "string"}, "pending_status": map[string]any{"type": "string"}, "run_id": map[string]any{"type": "string"}, "run_status": map[string]any{"type": "string"}, "routine_definition_at_enqueue": digest, "routine_changed_since_publication": map[string]any{"type": "boolean"}, "routine_revision_pinned": map[string]any{"type": "boolean", "enum": []bool{false}}}}},

		"GET /api/v1/pages/{slug}/project/history":            {Response: map[string]any{"type": "object", "properties": map[string]any{"revisions": map[string]any{"type": "array", "items": historyItem}, "next_before": map[string]any{"type": "integer", "minimum": 0}}}},
		"GET /api/v1/pages/{slug}/project/history/{revision}": {Response: historical},
		"POST /api/v1/pages/{slug}/project/restore":           {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"revision", "expected_revision"}, "properties": map[string]any{"revision": revision, "expected_revision": revision}}, Response: map[string]any{"type": "object", "properties": map[string]any{"revision": revision, "digest": digest, "git_commit": checkpoint, "state": map[string]any{"type": "string", "enum": []string{"draft"}}}}},

		"GET /api/v1/pages/runtime/bootstrap":      {Response: map[string]any{"type": "string"}, ResponseMedia: []string{"text/html"}, SuccessStatuses: []string{"200"}},
		"POST /api/v1/pages/{slug}/project/build":  {SuccessStatuses: []string{"202"}, RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_revision"}, "properties": map[string]any{"expected_revision": revision}}, Response: job},
		"GET /api/v1/pages/{slug}/project/preview": {Response: map[string]any{"type": "object", "required": []string{"revision", "build", "runtime_url"}, "properties": map[string]any{"revision": revision, "build": nullableJob, "artifact": artifact, "runtime_url": map[string]any{"type": "string"}, "development_same_origin": map[string]any{"type": "boolean", "description": "Explicit operator setting for reviewed same-origin development demos; does not provide browser process isolation."}}}},
		"GET /api/v1/pages/{slug}/project":         {Response: map[string]any{"type": "object", "required": []string{"revision", "digest", "definition", "project"}, "properties": map[string]any{"revision": revision, "digest": digest, "git_commit": checkpoint, "definition": definition, "project": source}}},
		"PUT /api/v1/pages/{slug}/project": {
			RequestRequired: true,
			Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_revision", "project"}, "properties": map[string]any{
				"expected_revision": map[string]any{"type": "integer", "minimum": 0, "description": "0 creates the first draft. A stale revision returns 409."}, "project": source, "definition": definition,
			}},
			Response: map[string]any{"type": "object", "required": []string{"revision", "digest", "state"}, "properties": map[string]any{"revision": revision, "digest": digest, "git_commit": checkpoint, "state": map[string]any{"type": "string", "enum": []string{"draft"}}}},
		},
	}
}
