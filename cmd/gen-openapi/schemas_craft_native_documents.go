package main

func craftNativeDocumentSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	ref := func(n string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + n} }
	obj := func(p map[string]any, required ...string) map[string]any {
		out := map[string]any{"type": "object", "properties": p, "additionalProperties": false}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	version := func(zero bool) map[string]any {
		pattern := "^[1-9][0-9]{0,18}$"
		if zero {
			pattern = "^(0|[1-9][0-9]{0,18})$"
		}
		return map[string]any{"type": "string", "pattern": pattern, "maxLength": 19, "description": "Canonical bounded int64 decimal string; maximum 9223372036854775807. Never parse as a JavaScript number."}
	}
	pin := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"} }
	hash := func() map[string]any { return map[string]any{"type": "string", "pattern": "^sha256:[a-f0-9]{64}$"} }
	bytes := func() map[string]any {
		return map[string]any{"type": "string", "format": "byte", "maxLength": 44739244, "description": "Base64 of the complete native document, at most 32 MiB decoded; never an HTML export."}
	}
	properties := func() map[string]any {
		return map[string]any{"workspaceId": str(), "pageId": str(), "applicationId": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "enginePin": pin(), "sessionId": str(), "generation": version(false), "baseVersion": version(false), "editorRevision": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Opaque snapshot revision/RPC correlation binding, at most 256 UTF-8 bytes."}, "contentHash": hash(), "bytes": bytes(), "targetVersion": version(false)}
	}
	required := []string{"workspaceId", "pageId", "applicationId", "enginePin", "sessionId", "generation", "baseVersion"}
	// attemptId identifies one save attempt so a reconcile can retire it (#4899).
	attempt := func() map[string]any {
		p := properties()
		p["attemptId"] = map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{8,64}$", "description": "Optional client nonce of one save attempt. Reconcile with it, by the live lease holder, answers 410 when version base+1 is not this attempt and retires the attempt, so it can never commit later; a save of a retired attempt is refused with 409. Without the live lease or beyond 8 retired attempts per page nothing is recorded and reconcile answers 409 (unconfirmed)."}
		return p
	}
	snapshot := obj(attempt(), append(append([]string{}, required...), "editorRevision", "contentHash", "bytes")...)
	reconcile := obj(attempt(), append(append([]string{}, required...), "editorRevision", "contentHash")...)
	restore := obj(properties(), append(append([]string{}, required...), "targetVersion")...)
	receipt := properties()
	delete(receipt, "bytes")
	delete(receipt, "targetVersion")
	receipt["version"] = version(false)
	receipt["candidateHash"] = hash()
	receipt["durability"] = map[string]any{"type": "string", "enum": []string{"node"}}
	receipt["issuedBy"] = map[string]any{"type": "string", "enum": []string{"crewship"}}
	receipt["phase"] = map[string]any{"type": "string", "enum": []string{"committed"}}
	receipt["unchanged"] = map[string]any{"type": "boolean", "enum": []bool{true}, "description": "No version was created: version equals baseVersion, which already holds this snapshot."}
	receiptRequired := append(append([]string{}, required...), "editorRevision", "contentHash", "version", "candidateHash", "durability", "issuedBy", "phase")
	quota := obj(map[string]any{"usedBytes": version(true), "limitBytes": version(false), "warning": map[string]any{"type": "boolean"}, "level": map[string]any{"type": "string", "enum": []string{"none", "near", "critical", "full"}}, "retention": str()}, "usedBytes", "limitBytes", "warning", "retention")
	engineBuild := map[string]any{"type": "string", "description": "Qualified workbook engine build, when named by the installed package."}
	document := obj(map[string]any{"version": version(false), "bytes": bytes(), "applicationId": properties()["applicationId"], "enginePin": pin(), "engineBuild": engineBuild, "editorBundle": pin(), "editorURL": str(), "format": map[string]any{"type": "string", "enum": []string{"xlsx", "xlsm"}}, "legacySource": map[string]any{"type": "boolean", "description": "The original native Sheet remains available for download."}, "quota": ref("NativeCraftDocumentQuota")}, "version", "bytes", "applicationId", "enginePin", "editorBundle", "editorURL", "quota")
	saveReceipt := map[string]any{}
	for k, v := range receipt {
		saveReceipt[k] = v
	}
	saveReceipt["signature_invalidated"] = map[string]any{"type": "boolean", "description": "A workbook Sheet only: its XML package signature was kept byte for byte but no longer matches the changed workbook; absent when it has none."}
	schemas := map[string]any{"NativeCraftSnapshotRequest": snapshot, "NativeCraftReconcileRequest": reconcile, "NativeCraftRestoreRequest": restore, "NativeCraftSnapshotReceipt": obj(saveReceipt, receiptRequired...), "NativeCraftDocumentQuota": quota, "NativeCraftDocument": document, "NativeCraftRestoreReceipt": obj(map[string]any{"version": version(false), "durability": receipt["durability"], "issuedBy": receipt["issuedBy"], "phase": receipt["phase"]}, "version", "durability", "issuedBy", "phase")}
	schemas["NativeCraftInitializeRequest"] = obj(map[string]any{"application_id": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "bytes": bytes()}, "application_id", "bytes")
	schemas["NativeCraftInitializeReceipt"] = obj(map[string]any{"session_token": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "description": "Private process-local human admission, issued only after atomic initialization. Never send to the editor iframe or local journal."}, "session_id": str(), "application_id": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "page_id": str(), "generation": version(false), "version": map[string]any{"type": "string", "enum": []string{"1"}}, "contentHash": map[string]any{"type": "string", "pattern": "^sha256:[a-f0-9]{64}$", "description": "A workbook Sheet started from an uploaded file: that file's SHA-256, computed by the server."}, "format": map[string]any{"type": "string", "enum": []string{"xlsx", "xlsm"}, "description": "A workbook Sheet started from an uploaded file: the file's format."}, "chatRef": map[string]any{"type": "boolean", "description": "The Sheet was added to the chat named by X-Craft-Chat, in the transaction that stored version 1."}}, "session_token", "session_id", "application_id", "page_id", "generation", "version")
	integer := func() map[string]any { return map[string]any{"type": "integer", "format": "int64"} }
	proposalState := map[string]any{"type": "string", "enum": []string{"open", "accepted", "rejected", "stale"}}
	schemas["NativeCraftDecider"] = obj(map[string]any{"actor_id": map[string]any{"type": "string", "description": "Recorded decider, `user:<id>` or `system:superseded`."}, "display_name": str(), "kind": map[string]any{"type": "string", "enum": []string{"human", "system"}}}, "actor_id", "display_name", "kind")
	schemas["NativeCraftProposal"] = obj(map[string]any{"id": map[string]any{"type": "string", "pattern": "^ncp_[a-f0-9]{24}$"}, "author": ref("NativeCraftHolder"), "base_version": integer(), "summary": map[string]any{"type": "string", "maxLength": 500}, "created_at": str(), "state": proposalState, "result_version": integer(), "decided_by": map[string]any{"allOf": []any{ref("NativeCraftDecider")}, "description": "Who decided a non-open proposal. kind human: the person who accepted or rejected it, or whose accept found the document content changed and recorded it stale. kind system (system:superseded): only when a newer proposal's sweep marked it stale before anyone decided. Absent while open."}, "decided_at": map[string]any{"type": "string", "format": "date-time", "description": "When the proposal was decided. Absent while open."}}, "id", "author", "base_version", "summary", "created_at", "state")
	schemas["NativeCraftProposalList"] = obj(map[string]any{"proposals": map[string]any{"type": "array", "maxItems": 16, "items": ref("NativeCraftProposal")}}, "proposals")
	schemas["NativeCraftProposalDocument"] = obj(map[string]any{"proposal": ref("NativeCraftProposal"), "bytes": bytes(), "content_hash": hash(), "engine_pin": pin()}, "proposal", "bytes", "content_hash", "engine_pin")
	schemas["NativeCraftProposalAcceptRequest"] = obj(map[string]any{"sessionId": str(), "generation": version(false)}, "sessionId", "generation")
	schemas["NativeCraftProposalAcceptReceipt"] = obj(map[string]any{"proposal": ref("NativeCraftProposal"), "receipt": ref("NativeCraftSnapshotReceipt")}, "proposal", "receipt")
	routes := map[string]DomainSchema{}
	routes["POST /api/v1/pages/{slug}/craft/initialize"] = DomainSchema{Request: ref("NativeCraftInitializeRequest"), Response: ref("NativeCraftInitializeReceipt"), RequestRequired: true, SuccessStatuses: []string{"201"}, Parameters: []map[string]any{
		{"name": "X-Craft-Chat", "in": "header", "schema": str(), "description": "Optional chat owned by the caller. Attach the new Sheet in the same transaction as version 1."},
	}}
	add := func(method, path, req, res string) {
		d := DomainSchema{Response: ref(res), SuccessStatuses: []string{"200"}, NoRequest: req == "", Parameters: []map[string]any{{"name": "X-Craft-Session", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "description": "Private original-human admission for this exact Page; sessionId in JSON is the distinct public connection ID."}}}
		if req != "" {
			d.Request = ref(req)
			d.RequestRequired = true
		}
		routes[method+" /api/v1/pages/{slug}/craft/document"+path] = d
	}
	schemas["NativeCraftWorkbookMigrationReceipt"] = obj(map[string]any{"version": version(false)}, "version")
	add("POST", "/migrate-workbook", "", "NativeCraftWorkbookMigrationReceipt")
	add("GET", "/original-workbook", "", "NativeCraftDocument")
	original := routes["GET /api/v1/pages/{slug}/craft/document/original-workbook"]
	original.Response = map[string]any{"type": "string", "format": "binary", "description": "Exact original native Sheet JSON bytes, returned as an attachment."}
	routes["GET /api/v1/pages/{slug}/craft/document/original-workbook"] = original

	add("GET", "", "", "NativeCraftDocument")
	add("POST", "/save", "NativeCraftSnapshotRequest", "NativeCraftSnapshotReceipt")
	saveKey := "POST /api/v1/pages/{slug}/craft/document/save"
	save := routes[saveKey]
	save.Parameters = append(save.Parameters,
		map[string]any{"name": "X-Craft-Touched", "in": "header", "schema": map[string]any{"type": "string"}, "description": "Workbook Sheet only. ASCII JSON list of at most 2000 sheet-qualified ranges, up to 64 KiB, or unknown."},
		map[string]any{"name": "X-Craft-Rebase", "in": "header", "schema": map[string]any{"type": "string", "enum": []string{"keep-local"}}, "description": "Workbook Sheet only. Audits an explicit choice to keep the local snapshot. The base still must equal the head."},
		map[string]any{"name": "X-Craft-Uncompressed-Length", "in": "header", "schema": map[string]any{"type": "string"}, "description": "Required with Content-Encoding: gzip. Exact JSON request length after decompression."},
	)
	routes[saveKey] = save
	add("POST", "/import", "NativeCraftSnapshotRequest", "NativeCraftSnapshotReceipt")
	add("POST", "/reconcile", "NativeCraftReconcileRequest", "NativeCraftSnapshotReceipt")
	add("POST", "/restore", "NativeCraftRestoreRequest", "NativeCraftRestoreReceipt")
	schemas["NativeCraftUndoRequest"] = obj(map[string]any{"operation_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 300, "description": "The operation_id of the agent change, as its apply result and the operations listing name it."}}, "operation_id")
	schemas["NativeCraftUndoReceipt"] = obj(map[string]any{"signature_invalidated": map[string]any{"type": "boolean", "description": "The workbook's XML package signature was kept byte for byte but no longer matches the changed workbook; absent when it has none."}, "version": version(false), "restored": map[string]any{"type": "integer", "minimum": 0, "description": "Cells that got their value from before the agent's change back."}, "skipped": map[string]any{"type": "integer", "minimum": 0, "description": "Cells kept because they were touched since the agent's change."}}, "version", "restored", "skipped")
	add("POST", "/undo", "NativeCraftUndoRequest", "NativeCraftUndoReceipt")
	undo := routes["POST /api/v1/pages/{slug}/craft/document/undo"]
	undo.SuccessStatuses = []string{"200", "201"}
	undo.Parameters = append(undo.Parameters, map[string]any{"name": "X-Craft-Generation", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[1-9][0-9]{0,18}$"}, "description": "The edit lease generation the caller holds."})
	routes["POST /api/v1/pages/{slug}/craft/document/undo"] = undo
	author := obj(map[string]any{"actorId": map[string]any{"type": "string", "description": "Recorded actor, `user:<id>`, `agent:<id>` or a system actor."}, "kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "system"}}, "displayName": str()}, "actorId", "kind", "displayName")
	schemas["NativeCraftVersionAuthor"] = author
	schemas["NativeCraftVersionProposal"] = obj(map[string]any{"id": map[string]any{"type": "string", "pattern": "^ncp_[a-f0-9]{24}$"}, "author": ref("NativeCraftVersionAuthor")}, "id", "author")
	versionProposal := map[string]any{"allOf": []any{ref("NativeCraftVersionProposal")}, "description": "Present when a person saved this version by accepting an agent proposal: the version's author is that person (approver and writer), proposal.author is the agent."}
	schemas["NativeCraftVersion"] = obj(map[string]any{"version": version(false), "createdAt": str(), "author": ref("NativeCraftVersionAuthor"), "restoredFrom": version(false), "proposal": versionProposal, "current": map[string]any{"type": "boolean", "description": "True only for the newest version, the one the editor opens."}, "format": map[string]any{"type": "string", "enum": []string{"xlsx", "xlsm", "native"}, "description": "Workbook Sheets only: the version's file format; native marks a version from before workbook files (readable, not restorable)."}, "contentHash": map[string]any{"type": "string", "pattern": "^sha256:[a-f0-9]{64}$", "description": "Workbook Sheets only: SHA-256 of the version's file."}, "size": map[string]any{"type": "string", "pattern": "^[0-9]+$", "description": "Workbook Sheets only: the version's file size in bytes."}}, "version", "createdAt", "author", "current")
	schemas["NativeCraftVersionList"] = obj(map[string]any{"currentVersion": version(false), "versions": map[string]any{"type": "array", "maxItems": 200, "description": "Newest first. Metadata only; never document bytes.", "items": ref("NativeCraftVersion")}, "truncated": map[string]any{"type": "boolean", "description": "True when older versions exist beyond this listing. They stay stored and restorable."}}, "currentVersion", "versions", "truncated")
	add("GET", "/versions", "", "NativeCraftVersionList")
	opsVersion := obj(map[string]any{
		"version":               version(false),
		"base_version":          version(false),
		"actor_kind":            map[string]any{"type": "string", "enum": []string{"agent", "human", "system"}},
		"actor_name":            map[string]any{"type": "string", "description": "The display name of the agent, or of the person who undid an agent change, that saved this version; empty otherwise."},
		"operation_id":          str(),
		"engine_pin":            str(),
		"calculate":             map[string]any{"type": "string", "enum": []string{"saved", "calculated"}},
		"replayable":            map[string]any{"type": "boolean"},
		"ops":                   map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "The resolved engine commands (addresses and literals) the agent change applied, in order; present only when replayable. Carries cell content."},
		"digest":                str(),
		"signature_invalidated": map[string]any{"type": "boolean", "description": "The workbook's XML package signature was kept byte for byte but no longer matches the changed workbook; absent when it has none."},
	}, "version", "base_version", "actor_kind", "actor_name", "replayable")
	schemas["NativeCraftOperationsVersion"] = opsVersion
	schemas["NativeCraftOperations"] = obj(map[string]any{
		"from":              version(false),
		"to":                version(false),
		"head":              version(false),
		"head_content_hash": map[string]any{"type": "string", "pattern": "^sha256:[a-f0-9]{64}$", "description": "SHA-256 of the head's stored workbook bytes."},
		"replayable":        map[string]any{"type": "boolean", "description": "Every version after from has its operations, so they replay over the editor's own work."},
		"versions":          map[string]any{"type": "array", "maxItems": 50, "items": ref("NativeCraftOperationsVersion"), "description": "Oldest first, from+1 to the head."},
	}, "from", "to", "head", "head_content_hash", "replayable", "versions")
	add("GET", "/operations", "", "NativeCraftOperations")
	schemas["NativeCraftVersionDocument"] = obj(map[string]any{"version": version(false), "currentVersion": version(false), "current": map[string]any{"type": "boolean", "description": "True when this version is the current head."}, "createdAt": str(), "author": ref("NativeCraftVersionAuthor"), "restoredFrom": version(false), "proposal": versionProposal, "applicationId": properties()["applicationId"], "enginePin": pin(), "engineBuild": engineBuild, "editorBundle": pin(), "editorURL": str(), "format": map[string]any{"type": "string", "enum": []string{"xlsx", "xlsm"}}, "bytes": bytes()}, "version", "currentVersion", "current", "createdAt", "author", "applicationId", "enginePin", "editorBundle", "editorURL", "bytes")
	add("GET", "/versions/{version}", "", "NativeCraftVersionDocument")
	exportVersion := map[string]any{"name": "version", "in": "query", "required": false, "schema": map[string]any{"type": "string", "pattern": "^[1-9][0-9]{0,18}$"}, "description": "Saved version to export; omitted, the confirmed head. Needs the `craft_version_history` workspace flag, like Read Craft document version."}
	routes["GET /api/v1/pages/{slug}/craft/document/export.xlsx"] = DomainSchema{NoRequest: true, Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}, SuccessStatuses: []string{"200"}, Parameters: []map[string]any{routes["GET /api/v1/pages/{slug}/craft/document"].Parameters[0], exportVersion}}
	session := map[string]any{"name": "X-Craft-Session", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "description": "Private original-human admission for this exact Page."}
	proposals := "/api/v1/pages/{slug}/craft/proposals"
	routes["GET "+proposals] = DomainSchema{NoRequest: true, Response: ref("NativeCraftProposalList"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	routes["GET "+proposals+"/{proposalID}"] = DomainSchema{NoRequest: true, Response: ref("NativeCraftProposalDocument"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	routes["POST "+proposals+"/{proposalID}/accept"] = DomainSchema{Request: ref("NativeCraftProposalAcceptRequest"), RequestRequired: true, Response: ref("NativeCraftProposalAcceptReceipt"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	routes["POST "+proposals+"/{proposalID}/reject"] = DomainSchema{Request: ref("NativeCraftEmptyRequest"), Response: ref("NativeCraftProposal"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	return routes, schemas
}
