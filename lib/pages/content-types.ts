import compiledDescriptors from "../../internal/pages/content_types.json" with { type: "json" }

export interface ContentTypeDescriptor {
  type_id: string
  schema_version: number
  viewer: string
  editor: string
  storage_adapter: string
  projection_adapter: string
  anchor_kinds: string[]
  export_formats: string[]
  execution_profile: string
  authoring_skill: string
}
export interface ContentReference {
  type_id: string
  schema_version: number
  supported: boolean
  reason?: string
}
export interface ContentFacts {
  has_slides?: boolean
  slides_schema?: number
  has_document?: boolean
  document_schema?: number
  has_board?: boolean
  board_format?: string
  has_artifact?: boolean
  artifact_profile?: string
  artifact_version?: number
  has_project?: boolean
  has_application?: boolean
  has_panels?: boolean
  human_owned?: boolean
}
export interface ContentDerivation {
  content_descriptor: ContentReference
  application_descriptor?: ContentReference
  can_initialize_content: boolean
}
// Values come from the same compiled JSON as Go. The cross-language check also
// compares actual Go/TS normalized values and derivation, not two snapshots.
export function contentTypes(): ContentTypeDescriptor[] {
  return structuredClone(compiledDescriptors)
}
export function lookupContentType(id: unknown, version: unknown): ContentTypeDescriptor | undefined {
  return contentTypes().find(value => value.type_id === id && value.schema_version === version)
}
function reference(type_id: string, schema_version: number): ContentReference {
  const supported = Boolean(lookupContentType(type_id, schema_version))
  return { type_id, schema_version, supported, ...(!supported ? { reason: "unsupported-version" } : {}) }
}
export function deriveContentTypes(facts: ContentFacts): ContentDerivation {
  const result: ContentDerivation = { content_descriptor: reference("panels", 1), can_initialize_content: false }
  const specialized = [facts.has_slides, facts.has_document, facts.has_board, facts.has_artifact].filter(Boolean).length
  if (specialized > 1 || (specialized > 0 && (facts.has_panels || facts.has_project || facts.has_application))) {
    result.content_descriptor = { type_id: "conflicting", schema_version: 0, supported: false, reason: "conflicting-storage" }
    return result
  }
  if (facts.has_slides) result.content_descriptor = { type_id: "slides", schema_version: facts.slides_schema ?? 0, supported: false, reason: "viewer-unavailable" }
  else if (facts.has_document) result.content_descriptor = reference("doc", facts.document_schema ?? 0)
  else if (facts.has_board) {
    result.content_descriptor = reference("board", 1)
    if (facts.board_format !== "crewship-board-excalidraw-0.18.1-v1") Object.assign(result.content_descriptor, { supported: false, reason: "unsupported-profile" })
  } else if (facts.has_artifact) {
    result.content_descriptor = reference("html", facts.artifact_version ?? 0)
    if (facts.artifact_profile !== "static-html-v1") Object.assign(result.content_descriptor, { supported: false, reason: "unsupported-profile" })
  } else {
    if (facts.has_project || facts.has_application) result.application_descriptor = reference("application", 1)
    result.can_initialize_content = Boolean(facts.human_owned && !facts.has_panels && !facts.has_project && !facts.has_application)
  }
  return result
}
export function resolveContentReference(value: unknown): ContentTypeDescriptor | undefined {
  if (!value || typeof value !== "object") return undefined
  const reference = value as Partial<ContentReference>
  if (reference.supported !== true) return undefined
  return lookupContentType(reference.type_id, reference.schema_version)
}

export interface PageContentSource extends ContentFacts {
  content_descriptor?: unknown
  application_descriptor?: unknown
}
// Missing metadata is old-server compatibility. Explicit unknown metadata
// always fails closed; old presence flags must never override it.
export function resolvePageContent(source: PageContentSource) {
  const legacy = deriveContentTypes({ ...source, document_schema: 2, board_format: "crewship-board-excalidraw-0.18.1-v1", artifact_profile: "static-html-v1", artifact_version: 1 })
  const primary = resolveContentReference(Object.prototype.hasOwnProperty.call(source, "content_descriptor") ? source.content_descriptor : legacy.content_descriptor)
  const applicationReference = Object.prototype.hasOwnProperty.call(source, "content_descriptor") || Object.prototype.hasOwnProperty.call(source, "application_descriptor") ? source.application_descriptor : legacy.application_descriptor
  const application = applicationReference === undefined || applicationReference === null ? undefined : resolveContentReference(applicationReference)
  // Presence facts are still sent by both APIs. An explicit descriptor may
  // narrow support, but cannot contradict storage or hide an existing source.
  const samePrimary = primary?.type_id === legacy.content_descriptor.type_id
  const sameApplication = Boolean(application) === Boolean(legacy.application_descriptor) && (!application || application.type_id === "application")
  const supported = Boolean(primary && samePrimary && sameApplication && legacy.content_descriptor.supported && (applicationReference == null || application))
  return { primary, application, supported, canInitializeContent: supported && primary?.type_id === "panels" && !application && legacy.can_initialize_content }
}
