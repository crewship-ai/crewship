export interface StablePageReference {
  readonly kind: "page"
  readonly workspaceId: string
  readonly pageId: string
}

export type PageLinkReference = StablePageReference | { readonly kind: "invalid" }
const identityPattern = /^[A-Za-z0-9_-]{1,128}$/
const slugPattern = /^[a-z0-9][a-z0-9_-]{0,63}$/

export function readStablePageReference(search: string): PageLinkReference | undefined {
  const params = new URLSearchParams(search)
  if (!params.has("ref")) return undefined
  const pageId = params.get("ref") ?? ""
  const workspaceId = params.get("workspace_id") ?? ""
  if (params.getAll("ref").length !== 1 || params.getAll("workspace_id").length !== 1 ||
      !identityPattern.test(pageId) || !identityPattern.test(workspaceId)) return { kind: "invalid" }
  return { kind: "page", pageId, workspaceId }
}

export function samePageReference(a: PageLinkReference | undefined, b: PageLinkReference | undefined): boolean {
  if (!a || !b) return a === b
  if (a.kind === "invalid" || b.kind === "invalid") return a.kind === b.kind
  return a.pageId === b.pageId && a.workspaceId === b.workspaceId
}

export function stablePageHref(workspaceId: string, pageId: string): string {
  if (!identityPattern.test(workspaceId) || !identityPattern.test(pageId)) throw new Error("Confirmed Page and workspace identity required")
  return `/pages?${new URLSearchParams({ ref: pageId, workspace_id: workspaceId })}`
}

export function writePageReference(params: URLSearchParams, reference: PageLinkReference | undefined): void {
  // workspace_id belongs to this link only when paired with ref. Preserve
  // unrelated existing workspace query parameters on ordinary legacy routes.
  if (params.has("ref")) params.delete("workspace_id")
  params.delete("ref")
  if (reference?.kind === "page") {
    params.set("ref", reference.pageId)
    params.set("workspace_id", reference.workspaceId)
  } else if (reference?.kind === "invalid") {
    params.set("ref", "")
  }
}

export interface ResolvedPageIdentity { page_id: string; slug: string }
export function resolvedPageIdentity(value: unknown, expectedPageId: string): ResolvedPageIdentity {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Page link is unavailable")
  const identity = value as Record<string, unknown>
  if (identity.page_id !== expectedPageId || typeof identity.slug !== "string" || !slugPattern.test(identity.slug)) throw new Error("Page link is unavailable")
  return { page_id: expectedPageId, slug: identity.slug }
}
