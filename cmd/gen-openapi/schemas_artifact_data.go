package main

func artifactDashboardDataSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	counter := func(min int) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min, "maximum": 9007199254740991}
	}
	text := func() map[string]any { return map[string]any{"type": "string"} }
	identifier := func() map[string]any {
		return map[string]any{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$"}
	}
	described := func(schema map[string]any, text string) map[string]any {
		schema["description"] = text
		return schema
	}
	array := func(item string, max int) map[string]any {
		return map[string]any{"type": "array", "maxItems": max, "items": ref(item)}
	}
	value := map[string]any{"type": "object", "additionalProperties": true, "description": "At most 256 KiB of JSON; duplicate keys and nesting beyond 64 are rejected. Numbers are preserved without a floating-point conversion."}
	writeCounter := counter(0)
	writeCounter["maximum"] = 9007199254740990
	writeCounter["description"] = "Expected stored version of this document: 0 when it must not exist yet, otherwise its exact current version. Any mismatch rejects the whole batch with 409 artifact_data_conflict."
	writes := array("ArtifactDataWrite", 50)
	writes["minItems"] = 1
	writes["description"] = "1 to 50 documents with unique IDs; the whole request is limited to 1 MiB."
	schemas := map[string]any{
		"ArtifactDataWrite":         object(map[string]any{"id": identifier(), "ifVersion": writeCounter, "value": value}, "id", "ifVersion", "value"),
		"ArtifactDataBatch":         object(map[string]any{"base_version": described(counter(1), "Latest code version of the artifact. A different value returns 409 artifact_version_conflict and nothing is written."), "documents": writes, "run_id": described(map[string]any{"type": "string", "pattern": "^run_[a-f0-9]{24}$"}, "Optional active run owned by the authenticated producer. The run must still match the collection generation.")}, "base_version", "documents"),
		"ArtifactDataDocument":      object(map[string]any{"id": identifier(), "version": described(counter(1), "Stored version of this document; 1 after the first write and incremented by every write."), "value": value, "updated_at": map[string]any{"type": "string", "format": "date-time"}, "source_label": described(text(), "Display attribution chosen by the server: You, Contributor, Agent or Former contributor. Account, agent and run identifiers are never returned."), "source_kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "unknown"}}}, "id", "version", "value", "updated_at", "source_label", "source_kind"),
		"ArtifactDataResponse":      object(map[string]any{"collection": identifier(), "code_version": described(counter(1), "Code version whose dashboard.json declared the visible collections."), "documents": array("ArtifactDataDocument", 4096)}, "collection", "code_version", "documents"),
		"ArtifactDataCollection":    object(map[string]any{"name": identifier(), "documents": map[string]any{"type": "integer", "minimum": 0, "maximum": 4096, "description": "Stored documents; a collection holds at most 4096."}, "bytes": map[string]any{"type": "integer", "minimum": 0, "maximum": 8388608, "description": "Stored JSON bytes; a collection holds at most 8 MiB."}}, "name", "documents", "bytes"),
		"ArtifactDataBatchResponse": object(map[string]any{"collection": identifier(), "code_version": described(counter(1), "Code version whose dashboard.json declared the visible collections."), "documents": array("ArtifactDataDocument", 50), "generation": described(map[string]any{"type": "integer", "format": "int64", "minimum": 0}, "The collection's data generation after this batch: the sum of its document versions, which only grows. If run_id was supplied, name it as data_generation when you finish that run as succeeded.")}, "collection", "code_version", "documents", "generation"),
		"ArtifactDataRunStart":      object(map[string]any{"base_version": described(counter(1), "Latest code version; it must declare the collection."), "idempotency_key": described(map[string]any{"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$"}, "Repeating a key returns the same run with 200; a key used by another producer returns 409.")}, "base_version", "idempotency_key"),
		"ArtifactDataRunFinish":     object(map[string]any{"base_version": described(counter(1), "Latest code version; it must declare the collection."), "state": map[string]any{"type": "string", "enum": []string{"succeeded", "failed"}}, "reason": described(map[string]any{"type": "string", "maxLength": 2000}, "Only with state failed. Stored cleaned: control characters and common credential patterns removed, at most 500 bytes."), "data_generation": described(map[string]any{"type": "integer", "format": "int64", "minimum": 0}, "Required with state succeeded, refused with failed: the generation returned by the producer's last batch linked with run_id. It must equal the collection's current generation, or 409 artifact_data_run_generation."), "unchanged": described(map[string]any{"type": "boolean"}, "With state succeeded: the run verified that no data needed to change. Required when data_generation equals the run's start_generation, refused otherwise.")}, "base_version", "state"),
		"ArtifactDataRun":           object(map[string]any{"id": map[string]any{"type": "string", "pattern": "^run_[0-9a-f]{24}$"}, "collection": identifier(), "state": map[string]any{"type": "string", "enum": []string{"running", "succeeded", "failed"}}, "started_by": described(text(), "Display attribution chosen by the server: You, Contributor, Agent or Former contributor. The producer is the authenticated principal and is never accepted from the client."), "started_by_kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "unknown"}}, "started_at": map[string]any{"type": "string", "format": "date-time"}, "start_generation": described(map[string]any{"type": "integer", "format": "int64", "minimum": 0}, "The collection's data generation when the run started."), "finished_at": map[string]any{"type": "string", "format": "date-time"}, "finished_by": described(text(), "Who finished the run, as display attribution like started_by. Only the producer can finish a run as succeeded; any writer can finish it as failed."), "finished_by_kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "unknown"}}, "data_generation": described(map[string]any{"type": "integer", "format": "int64", "minimum": 0}, "The collection's data generation when the run finished."), "partial": described(map[string]any{"type": "boolean"}, "True for a failed run during which the collection's data changed: batches apply as written, so some values may be from this run."), "failure_reason": described(text(), "Returned to the owner and editors only.")}, "id", "collection", "state", "started_by", "started_by_kind", "started_at", "start_generation"),
		"ArtifactDataRunSummary":    object(map[string]any{"collection": identifier(), "latest": ref("ArtifactDataRun"), "last_attempt_at": map[string]any{"type": "string", "format": "date-time"}, "last_success_at": described(map[string]any{"type": "string", "format": "date-time"}, "Finish time of the newest retained succeeded run; absent when none succeeded."), "active_runs": described(array("ArtifactDataRun", 16), "Every running run of the collection, newest first, so a run whose producer died stays findable. Returned to the owner and editors only.")}, "collection", "latest", "last_attempt_at"),
		"ArtifactDataRunError":      object(map[string]any{"error": text(), "code": text(), "blocking_runs": described(array("ArtifactDataRun", 16), "With 409 artifact_data_run_overlap: the runs that block this success. Any writer can finish one as failed.")}, "error"),
		"ArtifactDataRuns":          object(map[string]any{"code_version": counter(1), "stalled_after_seconds": described(map[string]any{"type": "integer", "format": "int64", "minimum": 60}, "Configured threshold after which clients show a running run as stalled (CREWSHIP_ARTIFACT_DATA_RUN_STALLED_AFTER, 1800 by default). A display default; the server never ends a run."), "runs": described(array("ArtifactDataRunSummary", 32), "One entry per collection declared in code_version that has at least one retained run.")}, "code_version", "stalled_after_seconds", "runs"),
		"ArtifactDataCollections":   object(map[string]any{"code_version": counter(1), "collections": described(array("ArtifactDataCollection", 32), "Collections declared by dashboard.json in code_version; an artifact has at most 32.")}, "code_version", "collections"),
	}
	routes := map[string]DomainSchema{}
	for _, entry := range []struct{ method, path, response string }{
		{"GET", "/api/v1/pages/{slug}/artifact/data", "ArtifactDataCollections"},
		{"GET", "/api/v1/pages/{slug}/artifact/data/{collection}", "ArtifactDataResponse"},
		{"POST", "/api/v1/pages/{slug}/artifact/data/{collection}", "ArtifactDataBatchResponse"},
	} {
		schema := DomainSchema{NoRequest: entry.method == "GET", Response: ref(entry.response), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}}
		if entry.method == "POST" {
			schema.Request = ref("ArtifactDataBatch")
			schema.RequestRequired = true
		}
		routes[entry.method+" "+entry.path] = schema
	}
	for _, entry := range []struct{ method, path, request, response string }{
		{"GET", "/api/v1/pages/{slug}/artifact/data-runs", "", "ArtifactDataRuns"},
		{"POST", "/api/v1/pages/{slug}/artifact/data/{collection}/runs", "ArtifactDataRunStart", "ArtifactDataRun"},
		{"POST", "/api/v1/pages/{slug}/artifact/data/{collection}/runs/{run}", "ArtifactDataRunFinish", "ArtifactDataRun"},
	} {
		schema := DomainSchema{NoRequest: entry.request == "", Response: ref(entry.response), SuccessStatuses: []string{"200"}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "500", "503"}}
		if entry.request != "" {
			schema.Request = ref(entry.request)
			schema.RequestRequired = true
		}
		if entry.request == "ArtifactDataRunFinish" {
			schema.ErrorResponse = ref("ArtifactDataRunError")
		}
		if entry.request == "ArtifactDataRunStart" {
			schema.SuccessStatuses = []string{"200", "201"}
			schema.ErrorStatuses = append(schema.ErrorStatuses, "413")
		}
		routes[entry.method+" "+entry.path] = schema
	}
	return routes, schemas
}
