// Editor completions, read out of the routine JSON Schema.
//
// schemas/routine.v1.json is generated from the Go DSL structs and is
// already the published contract (`crewship routine schema`). Reading
// completions from it means a step kind added to
// internal/pipeline/types.go reaches the editor without anyone
// remembering a second list — and a list that must be remembered is a
// list that goes stale. The FE StepKind union drifting from the Go one
// is exactly how `foreach` and `query` ended up drawn as agent nodes.
//
// The one thing NOT derived is the prose: the schema does not describe
// its own type enum. Those sentences are authored below, and a test
// reddens if the schema gains a kind nobody has described.

import schema from "@/schemas/routine.v1.json"

export interface CompletionItem {
  key: string
  detail: string
  required?: boolean
}

export interface StepKindItem {
  kind: string
  detail: string
}

interface JsonSchemaNode {
  type?: string
  description?: string
  enum?: string[]
  required?: string[]
  properties?: Record<string, JsonSchemaNode>
  items?: JsonSchemaNode
  $ref?: string
}

const doc = schema as unknown as {
  properties?: Record<string, JsonSchemaNode>
  $defs?: Record<string, JsonSchemaNode>
}

/** What each step kind is for, in one line. Authored, not derived. */
const KIND_DETAIL: Record<string, string> = {
  agent_run: "Agent rozhodne — nepředvídatelné z definice, jen auditovatelné zpětně",
  call_pipeline: "Zavolá jiný recept jako podproces",
  http: "Jedno HTTP volání na známý endpoint — deterministické",
  code: "Spustí inline kód v sandboxu",
  wait: "Zaparkuje běh, dokud člověk nebo událost nerozhodne",
  transform: "Čistá funkce nad výstupem předchozího kroku — deterministické",
  notify: "Zapíše kartu do inboxu a rozešle ji kanály dle kategorie",
  script: "Spustí soubor ze sdíleného adresáře posádky — deterministické",
  query: "Read-only agregace nad daty běhů — deterministické",
  foreach: "Smyčka — tělo se spustí jednou za položku",
}

/** Sub-object each kind carries its body in, when it has one. */
const KIND_BODY_DEF: Record<string, string> = {
  http: "HTTPStep",
  code: "CodeStep",
  wait: "WaitStep",
  transform: "TransformStep",
  notify: "NotifyStep",
  script: "ScriptStep",
  query: "QueryStep",
  foreach: "ForeachStep",
}

const stepDef = (): JsonSchemaNode => doc.$defs?.Step ?? {}

/** Step kinds the executor recognises, straight from the schema enum. */
export function stepKinds(): StepKindItem[] {
  const enumValues = stepDef().properties?.type?.enum ?? []
  return enumValues.map((kind) => ({ kind, detail: KIND_DETAIL[kind] ?? "" }))
}

/** Fields available on every step, whatever its kind. */
export function stepKeys(): CompletionItem[] {
  const def = stepDef()
  const required = new Set(def.required ?? [])
  return Object.entries(def.properties ?? {})
    // A kind's body object is offered by keysForKind once the kind is
    // known; listing all eight here would bury the fields that always
    // apply under seven that never do.
    .filter(([key]) => !(key in KIND_BODY_DEF))
    .map(([key, node]) => ({
      key,
      detail: firstLine(node.description),
      required: required.has(key),
    }))
}

/** Fields inside the body object of one kind. Empty when it has none. */
export function keysForKind(kind: string): CompletionItem[] {
  const defName = KIND_BODY_DEF[kind]
  if (!defName) return []
  const def = doc.$defs?.[defName]
  if (!def?.properties) return []
  const required = new Set(def.required ?? [])
  return Object.entries(def.properties).map(([key, node]) => ({
    key,
    detail: firstLine(node.description),
    required: required.has(key),
  }))
}

/** Routine-level fields. */
export function topLevelKeys(): CompletionItem[] {
  return Object.entries(doc.properties ?? {})
    // $schema is documented as informational and ignored at runtime;
    // completing it would invite people to set something inert.
    .filter(([key]) => key !== "$schema")
    .map(([key, node]) => ({ key, detail: firstLine(node.description) }))
}

function firstLine(description?: string): string {
  if (!description) return ""
  return description.split("\n")[0].trim()
}
