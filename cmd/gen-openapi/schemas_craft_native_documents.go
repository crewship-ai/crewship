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
	snapshot := obj(properties(), append(append([]string{}, required...), "editorRevision", "contentHash", "bytes")...)
	reconcile := obj(properties(), append(append([]string{}, required...), "editorRevision", "contentHash")...)
	restore := obj(properties(), append(append([]string{}, required...), "targetVersion")...)
	receipt := properties()
	delete(receipt, "bytes")
	delete(receipt, "targetVersion")
	receipt["version"] = version(false)
	receipt["candidateHash"] = hash()
	receipt["durability"] = map[string]any{"type": "string", "enum": []string{"node"}}
	receipt["issuedBy"] = map[string]any{"type": "string", "enum": []string{"crewship"}}
	receipt["phase"] = map[string]any{"type": "string", "enum": []string{"committed"}}
	receiptRequired := append(append([]string{}, required...), "editorRevision", "contentHash", "version", "candidateHash", "durability", "issuedBy", "phase")
	quota := obj(map[string]any{"usedBytes": version(true), "limitBytes": version(false), "warning": map[string]any{"type": "boolean"}, "level": map[string]any{"type": "string", "enum": []string{"none", "near", "critical", "full"}}, "retention": str()}, "usedBytes", "limitBytes", "warning", "retention")
	document := obj(map[string]any{"version": version(false), "bytes": bytes(), "applicationId": properties()["applicationId"], "enginePin": pin(), "editorBundle": pin(), "editorURL": str(), "quota": ref("NativeCraftDocumentQuota")}, "version", "bytes", "applicationId", "enginePin", "editorBundle", "editorURL", "quota")
	schemas := map[string]any{"NativeCraftSnapshotRequest": snapshot, "NativeCraftReconcileRequest": reconcile, "NativeCraftRestoreRequest": restore, "NativeCraftSnapshotReceipt": obj(receipt, receiptRequired...), "NativeCraftDocumentQuota": quota, "NativeCraftDocument": document, "NativeCraftRestoreReceipt": obj(map[string]any{"version": version(false), "durability": receipt["durability"], "issuedBy": receipt["issuedBy"], "phase": receipt["phase"]}, "version", "durability", "issuedBy", "phase")}
	schemas["NativeCraftInitializeRequest"] = obj(map[string]any{"application_id": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "bytes": bytes()}, "application_id", "bytes")
	schemas["NativeCraftInitializeReceipt"] = obj(map[string]any{"session_token": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "description": "Private process-local human admission, issued only after atomic initialization. Never send to the editor iframe or local journal."}, "session_id": str(), "application_id": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "page_id": str(), "generation": version(false), "version": map[string]any{"type": "string", "enum": []string{"1"}}}, "session_token", "session_id", "application_id", "page_id", "generation", "version")
	integer := func() map[string]any { return map[string]any{"type": "integer", "format": "int64"} }
	proposalState := map[string]any{"type": "string", "enum": []string{"open", "accepted", "rejected", "stale"}}
	schemas["NativeCraftProposal"] = obj(map[string]any{"id": map[string]any{"type": "string", "pattern": "^ncp_[a-f0-9]{24}$"}, "author": ref("NativeCraftHolder"), "base_version": integer(), "summary": map[string]any{"type": "string", "maxLength": 500}, "created_at": str(), "state": proposalState, "result_version": integer()}, "id", "author", "base_version", "summary", "created_at", "state")
	schemas["NativeCraftProposalList"] = obj(map[string]any{"proposals": map[string]any{"type": "array", "maxItems": 16, "items": ref("NativeCraftProposal")}}, "proposals")
	schemas["NativeCraftProposalDocument"] = obj(map[string]any{"proposal": ref("NativeCraftProposal"), "bytes": bytes(), "content_hash": hash(), "engine_pin": pin()}, "proposal", "bytes", "content_hash", "engine_pin")
	schemas["NativeCraftProposalAcceptRequest"] = obj(map[string]any{"sessionId": str(), "generation": version(false)}, "sessionId", "generation")
	schemas["NativeCraftProposalAcceptReceipt"] = obj(map[string]any{"proposal": ref("NativeCraftProposal"), "receipt": ref("NativeCraftSnapshotReceipt")}, "proposal", "receipt")
	routes := map[string]DomainSchema{}
	routes["POST /api/v1/pages/{slug}/craft/initialize"] = DomainSchema{Request: ref("NativeCraftInitializeRequest"), Response: ref("NativeCraftInitializeReceipt"), RequestRequired: true, SuccessStatuses: []string{"201"}}
	add := func(method, path, req, res string) {
		d := DomainSchema{Response: ref(res), SuccessStatuses: []string{"200"}, NoRequest: req == "", Parameters: []map[string]any{{"name": "X-Craft-Session", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "description": "Private original-human admission for this exact Page; sessionId in JSON is the distinct public connection ID."}}}
		if req != "" {
			d.Request = ref(req)
			d.RequestRequired = true
		}
		routes[method+" /api/v1/pages/{slug}/craft/document"+path] = d
	}
	add("GET", "", "", "NativeCraftDocument")
	add("POST", "/save", "NativeCraftSnapshotRequest", "NativeCraftSnapshotReceipt")
	add("POST", "/import", "NativeCraftSnapshotRequest", "NativeCraftSnapshotReceipt")
	add("POST", "/reconcile", "NativeCraftReconcileRequest", "NativeCraftSnapshotReceipt")
	add("POST", "/restore", "NativeCraftRestoreRequest", "NativeCraftRestoreReceipt")
	session := map[string]any{"name": "X-Craft-Session", "in": "header", "required": true, "schema": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "description": "Private original-human admission for this exact Page."}
	proposals := "/api/v1/pages/{slug}/craft/proposals"
	routes["GET "+proposals] = DomainSchema{NoRequest: true, Response: ref("NativeCraftProposalList"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	routes["GET "+proposals+"/{proposalID}"] = DomainSchema{NoRequest: true, Response: ref("NativeCraftProposalDocument"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	routes["POST "+proposals+"/{proposalID}/accept"] = DomainSchema{Request: ref("NativeCraftProposalAcceptRequest"), RequestRequired: true, Response: ref("NativeCraftProposalAcceptReceipt"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	routes["POST "+proposals+"/{proposalID}/reject"] = DomainSchema{Request: ref("NativeCraftEmptyRequest"), Response: ref("NativeCraftProposal"), SuccessStatuses: []string{"200"}, Parameters: []map[string]any{session}}
	return routes, schemas
}
