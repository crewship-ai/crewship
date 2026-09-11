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
	actorKind := map[string]any{"type": "string", "enum": []string{"user", "agent", "crew", "unknown"}}
	historyItem := map[string]any{"type": "object", "properties": map[string]any{"revision": revision, "digest": digest, "git_commit": checkpoint, "actor": map[string]any{"type": "string"}, "actor_kind": actorKind, "created_at": map[string]any{"type": "string", "format": "date-time"}, "restorable": map[string]any{"type": "boolean"}}}
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

	// The authorized review snapshot. Every nullable field below is nullable
	// because the database genuinely cannot answer it in some real state —
	// no candidate, no prior publication, no recorded routine digest — and a
	// zero value there would read as agreement.
	reviewActor := map[string]any{"type": "object", "description": "Who the candidate is attributed to: in draft mode the AUTHOR of the source revision, in ?publication=N mode the PUBLISHER that publication recorded. An erased publisher is kind \"unknown\".", "required": []string{"kind", "id"}, "properties": map[string]any{"kind": actorKind, "id": map[string]any{"type": "string"}, "label": map[string]any{"type": "string", "description": "Resolved at read time from the current directory; absent when the id no longer resolves."}}}
	reviewBuild := map[string]any{"type": "object", "nullable": true, "required": []string{"id", "state", "artifact_digest"}, "properties": map[string]any{"id": map[string]any{"type": "string"}, "state": map[string]any{"type": "string", "enum": []string{"running", "ready", "failed", "interrupted"}}, "artifact_digest": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"}}}
	reviewCandidate := map[string]any{"type": "object", "nullable": true, "description": "The current draft, or — with ?publication=N — retained publication N as a rollback candidate.", "required": []string{"revision", "git_commit", "source_digest", "created_at", "actor", "build", "definition"}, "properties": map[string]any{"revision": revision, "git_commit": checkpoint, "source_digest": digest, "created_at": map[string]any{"type": "string", "description": "In draft mode, when the source revision was saved. In ?publication=N mode, when that publication was made. Empty — never null, never invented — when the draft has no matching revision row and the server therefore cannot say; no format is declared because an empty string is not a date-time."}, "actor": reviewActor, "build": reviewBuild, "definition": map[string]any{"type": "object", "nullable": true, "description": "The Page document this candidate would make live — the draft's, or in ?publication=N mode the archived one that version recorded — read in the same request as baseline.definition and filtered by the same panel rule, so both sides of the comparison come from one instant and one decision. Null only when the stored document cannot be read as a Page document; when there is no candidate this whole object is null."}}}
	reviewBaseline := map[string]any{"type": "object", "required": []string{"publication_version", "published", "definition_digest", "definition", "excluded_panels", "withheld_changed", "source_revision", "git_commit", "source_available", "source_unavailable_reason"}, "properties": map[string]any{
		"publication_version": map[string]any{"type": "integer", "minimum": 0}, "published": map[string]any{"type": "boolean"},
		"definition_digest": digest, "source_revision": map[string]any{"type": "integer", "minimum": 1, "nullable": true}, "git_commit": map[string]any{"type": "string", "nullable": true},
		"definition":                map[string]any{"type": "object", "nullable": true, "description": "The live Page document, read from the same stored row and in the same request as definition_digest, so the comparison a reviewer reads and the value the publication is fenced on describe one instant. Panels this caller may not read are removed — panel visibility is its owning crew's, and an edit grant is not a read grant — and the same panels are removed from candidate.definition, so a withheld panel is absent from the comparison instead of appearing as one side adding it. Null only when the stored document cannot be read as a Page document; there is then no basis for a comparison."},
		"excluded_panels":           map[string]any{"type": "integer", "minimum": 0, "description": "How many panels were withheld from this comparison, counting a panel withheld from both documents once, so the screen can say its comparison is partial. Zero when neither document rendered. definition_digest is unaffected: it is always the digest of the full stored document."},
		"withheld_changed":          map[string]any{"type": "boolean", "description": "At least one withheld panel differs between the live definition and the candidate: added, removed, modified, or re-pointed between a crew this caller may see and one they may not. False means the comparison on screen covers everything that moves. True adds the withheld_change blocker and the publication is refused with 403 on every path, including rollback: reviewed_code=true attests that the whole change was reviewed, and a publisher who cannot read part of it cannot make that claim. Ownership is permission to perform the operation and is not evidence about what was read; a workspace administrator sees every panel, so this is never true for one. No flag waives it. Nothing about what changed is disclosed."},
		"source_available":          map[string]any{"type": "boolean"},
		"source_unavailable_reason": map[string]any{"type": "string", "nullable": true, "description": "A sentence shown where the comparison would have been. Unavailable is never the same as unchanged."},
	}}
	reviewRoutine := map[string]any{"type": "object", "required": []string{"routine", "published_digest", "current_digest", "state", "in_candidate"}, "properties": map[string]any{"routine": map[string]any{"type": "string"}, "published_digest": map[string]any{"type": "string", "nullable": true}, "current_digest": map[string]any{"type": "string", "nullable": true}, "state": map[string]any{"type": "string", "enum": []string{"unchanged", "changed", "unknown"}}, "in_candidate": map[string]any{"type": "boolean", "description": "This routine is declared by the candidate, so it belongs in expected_routine_digests. The list is a union and also carries routines only the live publication called; fencing on one of those is refused as a routines conflict naming a routine nobody moved."}}}
	reviewBlocker := map[string]any{"type": "object", "required": []string{"code", "message"}, "properties": map[string]any{"code": map[string]any{"type": "string", "enum": []string{"no_candidate", "candidate_matches_live", "build_missing", "build_failed", "build_stale", "baseline_unavailable", "routine_unresolved", "definition_moved", "withheld_change", "not_permitted", "storage_unavailable"}}, "message": map[string]any{"type": "string"}}}
	reviewSnapshot := map[string]any{"type": "object", "required": []string{"issued_at", "candidate", "baseline", "routines", "capabilities", "blockers", "initial_publication"}, "properties": map[string]any{
		"issued_at": map[string]any{"type": "string", "format": "date-time"}, "candidate": reviewCandidate, "baseline": reviewBaseline,
		"routines":            map[string]any{"type": "array", "items": reviewRoutine},
		"capabilities":        map[string]any{"type": "object", "required": []string{"may_edit_spec", "may_publish"}, "properties": map[string]any{"may_edit_spec": map[string]any{"type": "boolean"}, "may_publish": map[string]any{"type": "boolean"}}},
		"blockers":            map[string]any{"type": "array", "items": reviewBlocker},
		"initial_publication": map[string]any{"type": "boolean"},
	}}
	return map[string]DomainSchema{
		"POST /api/v1/pages/maintenance":                                     {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"discard_history": map[string]any{"type": "boolean"}, "confirm": map[string]any{"type": "boolean"}}}, Response: map[string]any{"type": "object", "properties": map[string]any{"compacted": map[string]any{"type": "boolean"}, "discarded_optional_history": map[string]any{"type": "boolean"}, "preserved": map[string]any{"type": "string"}}}},
		"GET /api/v1/pages/{slug}/project/fsck":                              {Response: map[string]any{"type": "object", "properties": map[string]any{"healthy": map[string]any{"type": "boolean"}, "checked_sources": map[string]any{"type": "integer"}, "checked_git_objects": map[string]any{"type": "integer"}, "checked_checkpoints": map[string]any{"type": "integer"}, "checked_artifacts": map[string]any{"type": "integer"}, "failures": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}, "error": map[string]any{"type": "string"}}}}}}},
		"GET /api/v1/pages/{slug}/application/panels/{panelId}/history":      {Response: map[string]any{"type": "object", "properties": map[string]any{"publication": revision, "next_before": map[string]any{"type": "integer", "minimum": 0}, "items": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "object", "properties": map[string]any{"sequence": revision, "data": map[string]any{}, "producedAt": map[string]any{"type": "string"}, "state": map[string]any{"type": "string"}}}}}}},
		"GET /api/v1/pages/{slug}/project/publications":                      {Response: map[string]any{"type": "object", "properties": map[string]any{"publications": map[string]any{"type": "array", "maxItems": 50, "items": publicationHistoryItem}, "publication_version": map[string]any{"type": "integer", "minimum": 0}, "published": map[string]any{"type": "boolean"}, "can_publish": map[string]any{"type": "boolean"}, "next_before": map[string]any{"type": "integer", "minimum": 0}}}},
		"POST /api/v1/pages/{slug}/project/unpublish":                        {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_publication"}, "properties": map[string]any{"expected_publication": revision}}, Response: map[string]any{"type": "object", "properties": map[string]any{"publication": map[string]any{"type": "object", "nullable": true}, "publication_version": revision, "published": map[string]any{"type": "boolean", "enum": []bool{false}}}}},
		"POST /api/v1/pages/{slug}/project/check":                            {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"build_id", "expected_revision"}, "properties": map[string]any{"build_id": map[string]any{"type": "string"}, "expected_revision": revision}}, Response: map[string]any{"type": "object", "properties": map[string]any{"build_id": map[string]any{"type": "string"}, "source_revision": revision, "git_commit": checkpoint, "artifact_digest": digest, "checks": map[string]any{"type": "object", "additionalProperties": true}}, "description": "Integrity, compiler and current binding checks. Browser and security review still required."}},
		"POST /api/v1/pages/{slug}/project/publish":                          {RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"expected_publication", "reviewed_code", "expected_definition_digest", "expected_routine_digests"}, "properties": map[string]any{"build_id": map[string]any{"type": "string"}, "expected_revision": revision, "expected_publication": map[string]any{"type": "integer", "minimum": 0}, "reviewed_code": map[string]any{"type": "boolean", "enum": []bool{true}, "description": "Attests that the whole change was reviewed. Refused with 403 when a panel the caller may not read differs between the live definition and the candidate — including on a rollback, which replaces the live definition too. That is not a state conflict and carries no conflict kind: refetching cannot change it, and no flag waives it. Read baseline.withheld_changed on the review snapshot before publishing."}, "rollback_version": revision, "expected_definition_digest": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "sha256 of the Page definition the publisher reviewed; a 409 with conflict:\"definition\" means it moved."}, "expected_routine_digests": map[string]any{"type": "object", "additionalProperties": digest, "description": "Reviewed routine definition digests keyed by routine slug; {} when the candidate declares no call actions. A 409 with conflict:\"routines\" names the ones that moved."}, "acknowledged_unavailable_baseline": map[string]any{"type": "boolean", "description": "Publish even though the live publication's retained source cannot be read back. Without it that case is refused with 409 and conflict:\"baseline\". It states that nobody compared the candidate with what is running; it proves nothing about the candidate, whose own integrity is verified either way, and it is recorded in the publication's checks as baseline_source. An initial publication has no prior source and never needs it."}}}, Response: receipt},
		"GET /api/v1/pages/{slug}/project/review":                            {Response: reviewSnapshot},
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
