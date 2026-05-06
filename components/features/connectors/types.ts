// Wire types for the connector catalog.
//
// These mirror the Go shapes in internal/connectors/manifest.go and
// internal/api/connectors_handler.go so the frontend can consume API
// responses without a bespoke transform layer. Keep field names in
// sync — the API serializes Go structs verbatim.

export type AuthMode =
  | "mcp_oauth"
  | "pat"
  | "conn_string"
  | "byo_oauth"
  | "none"

export type FieldType = "text" | "password" | "number" | "select" | "bool"

export interface ConnectorField {
  key: string
  label: string
  type: FieldType
  required: boolean
  default?: string
  placeholder?: string
  help?: string
  choices?: string[]
}

export interface ConnectorBrand {
  logo: string
  color: string
}

export interface ConnectorOAuth {
  authorization_url: string
  token_url: string
  scopes: string[]
  pkce: boolean
}

export interface ConnectorMCP {
  transport: "stdio" | "streamable-http"
  command?: string
  args?: string[]
  endpoint?: string
  env?: Record<string, string>
}

export interface ConnectorVerify {
  http?: {
    method: string
    url: string
    headers?: Record<string, string>
    expect_status: number
  }
  mcp_method?: string
}

export interface ConnectorDocs {
  setup_md: string
}

/** Full manifest — returned by GET /api/v1/connectors/{id}. */
export interface ConnectorManifest {
  id: string
  name: string
  description: string
  brand: ConnectorBrand
  category: string
  auth_mode: AuthMode
  fields?: ConnectorField[]
  oauth?: ConnectorOAuth
  mcp: ConnectorMCP
  derived?: Record<string, string>
  verify?: ConnectorVerify
  docs?: ConnectorDocs
}

/** Tile-shaped subset returned by GET /api/v1/connectors. */
export interface ConnectorListItem {
  id: string
  name: string
  description: string
  category: string
  auth_mode: AuthMode
  brand_logo: string
  brand_color: string
}

/** POST /api/v1/connectors/{id}/install response. */
export interface InstallResponse {
  integration_id: string
  next_step?: "" | "oauth" | "mcp_oauth"
  oauth_url?: string
}

/** POST /api/v1/connectors/{id}/verify response. */
export interface VerifyResponse {
  ok: boolean
  message?: string
}
