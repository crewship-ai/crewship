package main

// DomainSchema describes the JSON contract for one operation.  The generator
// deliberately keeps this catalog separate from route discovery: handlers can
// evolve their wire types without making the route scanner grow a second,
// domain-specific parser.
type DomainSchema struct {
	Request       map[string]any
	Response      map[string]any
	ResponseMedia []string
	// SuccessStatuses replaces inferred success statuses for handlers whose
	// success path is deliberately no-content (for example DELETE → 204).
	// A non-nil slice is significant; an empty slice means no success status.
	SuccessStatuses []string
}

// DomainSchemaMap returns the audited schemas for the API's operational
// domains. Keys inside a domain are "METHOD /path" strings, using the same
// ServeMux spelling as the route scanner. The returned maps are newly built on
// every call, so callers may enrich a document without changing the catalog.
func operationalDomainSchemaCatalog() map[string]map[string]DomainSchema {
	stringSchema := func() map[string]any { return map[string]any{"type": "string"} }
	boolSchema := func() map[string]any { return map[string]any{"type": "boolean"} }
	intSchema := func() map[string]any { return map[string]any{"type": "integer"} }
	objectSchema := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	arraySchema := func(items map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": items}
	}
	anyObject := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	list := func(item map[string]any) map[string]any { return arraySchema(item) }
	jsonRequest := func(properties map[string]any) map[string]any {
		return objectSchema(properties)
	}
	text := func() map[string]any { return map[string]any{"type": "string"} }
	binary := func() map[string]any { return map[string]any{"type": "string", "format": "binary"} }

	admin := map[string]DomainSchema{
		"GET /api/v1/admin/stats":               {Response: anyObject()},
		"GET /api/v1/admin/users":               {Response: list(anyObject())},
		"GET /api/v1/admin/workspaces":          {Response: list(anyObject())},
		"GET /api/v1/admin/health":              {Response: anyObject()},
		"GET /api/v1/admin/log-level":           {Response: objectSchema(map[string]any{"level": stringSchema()})},
		"PUT /api/v1/admin/log-level":           {Request: jsonRequest(map[string]any{"level": stringSchema()})},
		"GET /api/v1/admin/security-posture":    {Response: anyObject()},
		"GET /api/v1/admin/rate-limits":         {Response: list(anyObject())},
		"PUT /api/v1/admin/rate-limits/{key}":   {Request: jsonRequest(map[string]any{"limit": intSchema(), "window_seconds": intSchema()})},
		"GET /api/v1/admin/keeper/health":       {Response: anyObject()},
		"GET /api/v1/admin/keeper/config":       {Response: anyObject()},
		"PUT /api/v1/admin/keeper/config":       {Request: anyObject()},
		"GET /api/v1/admin/keeper/aux":          {Response: anyObject()},
		"GET /api/v1/admin/keeper/governance":   {Response: anyObject()},
		"PUT /api/v1/admin/keeper/governance":   {Request: anyObject()},
		"GET /api/v1/admin/users/{userId}/data": {Response: anyObject()},
		"POST /api/v1/admin/reencrypt":          {Response: objectSchema(map[string]any{"rewritten": intSchema(), "skipped": intSchema()})},
	}
	backups := map[string]DomainSchema{
		"GET /api/v1/admin/backups": {Response: list(anyObject())},
		"POST /api/v1/admin/backups": {Request: jsonRequest(map[string]any{
			"scope": stringSchema(), "scope_level": stringSchema(), "crew_id": stringSchema(),
			"passphrase": stringSchema(), "recipient": stringSchema(), "no_encrypt": boolSchema(), "output_dir": stringSchema(),
		}), Response: anyObject()},
		"GET /api/v1/admin/backups/status":   {Response: anyObject()},
		"GET /api/v1/admin/backups/metrics":  {Response: anyObject()},
		"GET /api/v1/admin/backups/inspect":  {Response: anyObject()},
		"GET /api/v1/admin/backups/verify":   {Response: anyObject()},
		"GET /api/v1/admin/backups/download": {Response: binary(), ResponseMedia: []string{"application/zstd"}},
		"POST /api/v1/admin/backups/restore": {Request: jsonRequest(map[string]any{
			"path": stringSchema(), "passphrase": stringSchema(), "identity": stringSchema(),
			"as_workspace": stringSchema(), "as_crew": stringSchema(), "replace": boolSchema(), "dry_run": boolSchema(), "files_only": boolSchema(),
		}), Response: anyObject()},
		"POST /api/v1/admin/backups/rotate":    {Response: anyObject()},
		"POST /api/v1/admin/backups/self-test": {Response: anyObject()},
	}
	memory := map[string]DomainSchema{
		"GET /api/v1/admin/memory/stats":                 {Response: anyObject()},
		"GET /api/v1/admin/memory/versions":              {Response: list(anyObject())},
		"GET /api/v1/admin/memory/versions/{id}/content": {Response: text(), ResponseMedia: []string{"text/markdown", "application/octet-stream"}},
		"GET /api/v1/admin/memory/config":                {Response: objectSchema(map[string]any{"versions_retention_days": intSchema()})},
		"PATCH /api/v1/admin/memory/config":              {Request: jsonRequest(map[string]any{"versions_retention_days": intSchema()}), Response: objectSchema(map[string]any{"versions_retention_days": intSchema()})},
		"GET /api/v1/memory/health":                      {Response: anyObject()},
		"GET /api/v1/memory/versions":                    {Response: list(anyObject())},
		"GET /api/v1/memory/versions/{sha}":              {Response: anyObject()},
		"POST /api/v1/memory/versions/{sha}/restore":     {Response: anyObject()},
		"GET /api/v1/memory/export":                      {Response: binary(), ResponseMedia: []string{"application/zip"}},
		"POST /api/v1/memory/import":                     {Request: jsonRequest(map[string]any{"crew_id": stringSchema(), "agent_slug": stringSchema(), "documents": arraySchema(anyObject())}), Response: anyObject()},
		"POST /api/v1/memory/search/hybrid":              {Request: jsonRequest(map[string]any{"query": stringSchema(), "limit": intSchema()}), Response: list(anyObject())},
	}
	notifications := map[string]DomainSchema{
		"GET /api/v1/notifications":           {Response: list(anyObject())},
		"GET /api/v1/notifications/count":     {Response: objectSchema(map[string]any{"count": intSchema()})},
		"GET /api/v1/notification-channels":   {Response: list(anyObject())},
		"POST /api/v1/notification-channels":  {Request: anyObject(), Response: anyObject()},
		"GET /api/v1/notification-providers":  {Response: list(anyObject())},
		"GET /api/v1/notification-templates":  {Response: list(anyObject())},
		"GET /api/v1/notification-deliveries": {Response: list(anyObject())},
		"GET /api/v1/me/notification-prefs":   {Response: anyObject()},
		"PUT /api/v1/me/notification-prefs":   {Request: anyObject(), Response: anyObject()},
	}
	integrations := map[string]DomainSchema{
		"GET /api/v1/integrations":                                 {Response: list(anyObject())},
		"GET /api/v1/integrations/crews":                           {Response: list(anyObject())},
		"GET /api/v1/integrations/composio/inventory":              {Response: anyObject()},
		"GET /api/v1/integrations/composio/toolkits":               {Response: list(anyObject())},
		"GET /api/v1/integrations/composio/tools":                  {Response: list(anyObject())},
		"GET /api/v1/integrations/composio/triggers":               {Response: list(anyObject())},
		"GET /api/v1/integrations/composio/triggers/active":        {Response: list(anyObject())},
		"POST /api/v1/integrations/composio/triggers":              {Request: objectSchema(map[string]any{"slug": stringSchema(), "user_id": stringSchema(), "config": anyObject()}), Response: anyObject()},
		"POST /api/v1/integrations/composio/connect":               {Request: objectSchema(map[string]any{"toolkit": stringSchema(), "user_id": stringSchema()}), Response: objectSchema(map[string]any{"redirect_url": stringSchema(), "connected_account_id": stringSchema(), "user_id": stringSchema()})},
		"GET /api/v1/integrations/composio/accounts/{accountId}":   {Response: anyObject()},
		"GET /api/v1/integrations/composio/agents/{agentId}/bind":  {Response: list(anyObject())},
		"POST /api/v1/integrations/composio/agents/{agentId}/bind": {Request: objectSchema(map[string]any{"user_id": stringSchema(), "apps": arraySchema(anyObject()), "toolkits": arraySchema(stringSchema())}), Response: anyObject()},
		"GET /api/v1/integrations/composio/default":                {Response: anyObject()},
		"PUT /api/v1/integrations/composio/default":                {Request: objectSchema(map[string]any{"enabled": boolSchema()}), Response: anyObject()},
		"GET /api/v1/oauth/providers":                              {Response: list(anyObject())},
		"POST /api/v1/oauth/initiate":                              {Request: objectSchema(map[string]any{"provider": stringSchema(), "redirect_uri": stringSchema()}), Response: anyObject()},
		"POST /api/v1/oauth/exchange":                              {Request: objectSchema(map[string]any{"code": stringSchema(), "state": stringSchema()}), Response: anyObject()},
	}
	filesMedia := map[string]DomainSchema{
		"GET /api/v1/agents/{agentId}/files":                       {Response: list(anyObject())},
		"GET /api/v1/agents/{agentId}/files/download":              {Response: binary(), ResponseMedia: []string{"application/octet-stream"}},
		"PUT /api/v1/agents/{agentId}/files/save":                  {Request: jsonRequest(map[string]any{"path": stringSchema(), "content": stringSchema()}), Response: anyObject()},
		"POST /api/v1/agents/{agentId}/chats/{chatId}/attachments": {Request: binary(), Response: anyObject()},
		"GET /api/v1/crews/{crewId}/files":                         {Response: list(anyObject())},
		"GET /api/v1/crews/{crewId}/files/download":                {Response: binary(), ResponseMedia: []string{"application/octet-stream"}},
		"PUT /api/v1/crews/{crewId}/files/save":                    {Request: jsonRequest(map[string]any{"path": stringSchema(), "content": stringSchema()}), Response: anyObject()},
		"GET /api/v1/agents/{agentId}/container-files":             {Response: list(anyObject())},
		"GET /api/v1/users/{id}/avatar":                            {Response: binary(), ResponseMedia: []string{"image/svg+xml"}},
		"POST /api/v1/users/me/avatar":                             {Request: binary(), Response: anyObject()},
	}
	authPublic := map[string]DomainSchema{
		"GET /api/health":                 {Response: objectSchema(map[string]any{"status": stringSchema()})},
		"GET /api/v1/system/setup-status": {Response: anyObject()},
		"GET /api/v1/system/telemetry":    {Response: anyObject()},
		"GET /api/v1/auth/google/status":  {Response: objectSchema(map[string]any{"enabled": boolSchema()})},
		"POST /api/v1/bootstrap":          {Request: anyObject(), Response: anyObject()},
		"POST /api/v1/auth/signup":        {Request: objectSchema(map[string]any{"full_name": stringSchema(), "email": stringSchema(), "password": stringSchema()}), Response: anyObject()},
		"POST /api/v1/auth/forgot":        {Request: objectSchema(map[string]any{"email": stringSchema()}), Response: anyObject()},
		"POST /api/v1/auth/reset":         {Request: objectSchema(map[string]any{"token": stringSchema(), "new_password": stringSchema()}), Response: anyObject()},
		"GET /api/v1/oauth/callback":      {Response: anyObject()},
	}
	system := map[string]DomainSchema{
		"GET /api/v1/system/runtime":    {Response: anyObject()},
		"GET /api/v1/system/version":    {Response: anyObject()},
		"GET /api/v1/system/license":    {Response: anyObject()},
		"GET /api/v1/system/keeper":     {Response: anyObject()},
		"GET /api/v1/system/aux-status": {Response: anyObject()},
		"GET /api/v1/runtime/capacity":  {Response: anyObject()},
		"GET /api/v1/crewshipd":         {Response: anyObject()},
	}
	return map[string]map[string]DomainSchema{
		"admin": admin, "backups": backups, "memory": memory,
		"notifications": notifications, "integrations": integrations,
		"files-media": filesMedia, "auth-public": authPublic, "system": system,
	}
}
