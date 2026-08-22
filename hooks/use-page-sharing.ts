"use client"

/**
 * Sharing a page outward: public links, panel webhooks, and deleting the page.
 *
 * Split from `use-page-grants.ts` rather than appended to it, and the split is
 * the same one the permission model itself draws. Grants answer "who INSIDE
 * this workspace reaches this page"; everything here answers "what leaves it" —
 * a link an accountant opens with no account, a token a CI job holds, or the
 * page ceasing to exist. Two questions, two files, and the one that governs
 * external exposure is worth reading on its own.
 *
 * Every one of these existed only as a `crewship page …` command until now. The
 * capability was never missing; the way to reach it from the app was. That is
 * why the wire types below mirror the server's exactly and add nothing: this is
 * a second door onto the same room, not a second room.
 *
 * ## The two things a caller here must not get wrong
 *
 *  1. **A secret is shown once.** `POST /public` and `POST /webhooks` are the
 *     only responses that ever carry `token` and `url`; every later read omits
 *     them, because the server stores a hash (`pages_public_tokens.go`,
 *     `pages_webhooks.go`). So the mutation's result has to reach the UI, and
 *     `onOk` forwards the RESPONSE rather than the request variables — the
 *     opposite of what the grant mutations do, and the reason is exactly this.
 *  2. **Revoked is not deleted.** A revoked link or token stays in its listing
 *     with a `revoked_at`, because the question after an incident is "was it
 *     used after we pulled it", and a deleted row cannot answer it. The list
 *     hooks therefore return everything and let the renderer say which is live.
 */

import { useQuery } from "@tanstack/react-query"

import { apiFetch } from "@/lib/api-fetch"
import {
  type UseApiMutationResult,
  useApiMutation,
} from "@/hooks/use-api-mutation"
import {
  type GatedRead,
  type WriteCallbacks,
  gateOf,
  pageQueryString,
  pageWriteFailureMessage,
} from "@/hooks/use-page-grants"

// ── the wire ──────────────────────────────────────────────────────────────

/** One public link, exactly as `pagePublicTokenWire` sends it. */
export interface WirePublicLink {
  id: string
  /** Present ONLY in the response that minted it. */
  token?: string
  /** Present ONLY in the response that minted it. */
  url?: string
  expires_at: string
  show_provenance: boolean
  has_password: boolean
  created_by: string
  created_at: string
  revoked_at?: string
  last_seen_at?: string
  /** The server's own verdict, computed from both columns and its own clock. */
  live: boolean
  /** Which panels this link serves — the ones declaring `public: true`. */
  panels: string[]
}

export interface WirePublicLinks {
  page: string
  tokens: WirePublicLink[]
}

/** One webhook token, exactly as `pageWebhookWire` sends it. */
export interface WireWebhook {
  id: string
  panel: string
  name?: string
  /** Present ONLY in the response that minted it. */
  token?: string
  /** Present ONLY in the response that minted it. */
  url?: string
  created_by: string
  created_at: string
  revoked_at?: string
  last_fired_at?: string
  fire_count: number
  live: boolean
}

export interface WireWebhooks {
  page: string
  webhooks: WireWebhook[]
}

// ── reads ─────────────────────────────────────────────────────────────────

async function fetchJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const res = await apiFetch(url, { signal })
  if (!res.ok) throw new Error(String(res.status))
  return (await res.json()) as T
}

export const pageSharingKeys = {
  links: (workspaceId: string, slug: string) =>
    ["page-public-links", workspaceId, slug] as const,
  webhooks: (workspaceId: string, slug: string) =>
    ["page-webhooks", workspaceId, slug] as const,
}

export interface UsePublicLinksResult extends GatedRead {
  links: WirePublicLink[]
}

/** GET the page's public links. Revoked ones are included, deliberately. */
export function usePagePublicLinks(
  workspaceId: string,
  slug: string | null,
): UsePublicLinksResult {
  const enabled = Boolean(workspaceId) && Boolean(slug)
  const query = useQuery({
    queryKey: pageSharingKeys.links(workspaceId, slug ?? ""),
    enabled,
    queryFn: ({ signal }) =>
      fetchJSON<WirePublicLinks>(
        `/api/v1/pages/${encodeURIComponent(slug ?? "")}/public${pageQueryString(workspaceId)}`,
        signal,
      ),
  })
  return { links: query.data?.tokens ?? [], ...gateOf(query.error, query.isPending, enabled) }
}

export interface UseWebhooksResult extends GatedRead {
  webhooks: WireWebhook[]
}

/** GET the page's webhook tokens. Revoked ones are included, deliberately. */
export function usePageWebhooks(
  workspaceId: string,
  slug: string | null,
): UseWebhooksResult {
  const enabled = Boolean(workspaceId) && Boolean(slug)
  const query = useQuery({
    queryKey: pageSharingKeys.webhooks(workspaceId, slug ?? ""),
    enabled,
    queryFn: ({ signal }) =>
      fetchJSON<WireWebhooks>(
        `/api/v1/pages/${encodeURIComponent(slug ?? "")}/webhooks${pageQueryString(workspaceId)}`,
        signal,
      ),
  })
  return { webhooks: query.data?.webhooks ?? [], ...gateOf(query.error, query.isPending, enabled) }
}

// ── writes ────────────────────────────────────────────────────────────────

export interface PublishVariables {
  /** Days until the link expires. Omitted sends no field, so the server's
   *  own default (30) applies rather than a number this form invented. */
  expiresInDays?: number
  /** Omitted means no password. Never logged, never put in a URL. */
  password?: string
  showProvenance?: boolean
}

/**
 * POST — mint a public link.
 *
 * `onOk` forwards the SERVER'S RESPONSE, not the variables: the token and the
 * URL exist in that one payload and nowhere else, ever again.
 */
export function usePagePublish(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<WirePublicLink> = {},
): UseApiMutationResult<PublishVariables, WirePublicLink> {
  return useApiMutation<PublishVariables, WirePublicLink>({
    request: (v) => ({
      input: `/api/v1/pages/${encodeURIComponent(slug)}/public${pageQueryString(workspaceId)}`,
      init: {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        // Each field is sent only when the author chose it. Sending
        // `expires_in_days: 30` explicitly would pin this form to a default
        // the server is free to change, and sending `password: ""` would be a
        // password rather than the absence of one.
        body: JSON.stringify({
          ...(v.expiresInDays !== undefined ? { expires_in_days: v.expiresInDays } : {}),
          ...(v.password ? { password: v.password } : {}),
          ...(v.showProvenance !== undefined ? { show_provenance: v.showProvenance } : {}),
        }),
      },
    }),
    invalidateKeys: [pageSharingKeys.links(workspaceId, slug)],
    onOk: (data) => cb.onOk?.(data as WirePublicLink),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}

export interface UnpublishVariables {
  id: string
}

/** DELETE — withdraw one public link. Immediate: the next request 404s. */
export function usePageUnpublish(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<UnpublishVariables> = {},
): UseApiMutationResult<UnpublishVariables, unknown> {
  return useApiMutation<UnpublishVariables, unknown>({
    request: (v) => ({
      input:
        `/api/v1/pages/${encodeURIComponent(slug)}/public/` +
        `${encodeURIComponent(v.id)}${pageQueryString(workspaceId)}`,
      init: { method: "DELETE" },
    }),
    invalidateKeys: [pageSharingKeys.links(workspaceId, slug)],
    onOk: (_data, v) => cb.onOk?.(v),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}

export interface WebhookCreateVariables {
  panel: string
  name?: string
}

/**
 * POST — mint a webhook token bound to exactly one panel.
 *
 * Same `onOk` contract as publish, and for the same reason: the token is in
 * this response and never in another.
 */
export function usePageWebhookCreate(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<WireWebhook> = {},
): UseApiMutationResult<WebhookCreateVariables, WireWebhook> {
  return useApiMutation<WebhookCreateVariables, WireWebhook>({
    request: (v) => ({
      input: `/api/v1/pages/${encodeURIComponent(slug)}/webhooks${pageQueryString(workspaceId)}`,
      init: {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ panel: v.panel, ...(v.name ? { name: v.name } : {}) }),
      },
    }),
    invalidateKeys: [pageSharingKeys.webhooks(workspaceId, slug)],
    onOk: (data) => cb.onOk?.(data as WireWebhook),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}

export interface WebhookRevokeVariables {
  id: string
}

/** DELETE — withdraw one webhook token. */
export function usePageWebhookRevoke(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<WebhookRevokeVariables> = {},
): UseApiMutationResult<WebhookRevokeVariables, unknown> {
  return useApiMutation<WebhookRevokeVariables, unknown>({
    request: (v) => ({
      input:
        `/api/v1/pages/${encodeURIComponent(slug)}/webhooks/` +
        `${encodeURIComponent(v.id)}${pageQueryString(workspaceId)}`,
      init: { method: "DELETE" },
    }),
    invalidateKeys: [pageSharingKeys.webhooks(workspaceId, slug)],
    onOk: (_data, v) => cb.onOk?.(v),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}

/**
 * DELETE — the page itself.
 *
 * No invalidation key here beyond the index: the thing this read from is gone,
 * and refetching a deleted page's grants would only produce a 404 to swallow.
 * The caller navigates away.
 */
export function usePageDelete(
  workspaceId: string,
  slug: string,
  cb: WriteCallbacks<void> = {},
): UseApiMutationResult<void, unknown> {
  return useApiMutation<void, unknown>({
    request: () => ({
      input: `/api/v1/pages/${encodeURIComponent(slug)}${pageQueryString(workspaceId)}`,
      init: { method: "DELETE" },
    }),
    invalidateKeys: [["pages", workspaceId]],
    onOk: () => cb.onOk?.(undefined),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => cb.onRefused?.(pageWriteFailureMessage(err)),
  })
}

// ── moving a page between workspaces ──────────────────────────────────────

/** One thing a bundle needs that the receiving workspace has to supply. */
export interface WireBundleRef {
  ref: string
  kind: string
  /** False for `script` and `webhook` producers — there is no table of scripts
   *  to bind them to, so they travel as declarations and need no mapping. */
  bindable: boolean
  used_by: string[]
}

/** A reference the import could not resolve. The import wrote nothing. */
export interface WireUnresolvedRef {
  ref: string
  kind: string
  used_by: string[]
  reason: string
}

export interface WirePageBundle {
  format: string
  page: { name: string; slug: string; description?: string; owner?: string; panels: unknown[] }
  references: WireBundleRef[]
  metadata: { exported_at: string; panel_count: number }
}

/**
 * GET the page as a portable bundle.
 *
 * Not a `useQuery`: an export is something a person ASKS for, once, to save —
 * caching it would hand somebody a file that silently predates the page they
 * are looking at. Called imperatively and the caller decides what to do with
 * the bytes.
 *
 * Export needs `write` authority rather than readership, because a bundle is
 * the whole spec including panels the caller might not be able to see the DATA
 * of — the server enforces that; this only reports its refusal.
 */
export async function fetchPageBundle(
  workspaceId: string,
  slug: string,
): Promise<WirePageBundle> {
  const res = await apiFetch(
    `/api/v1/pages/${encodeURIComponent(slug)}/export${pageQueryString(workspaceId)}`,
  )
  if (!res.ok) {
    let message = `Export failed (${res.status})`
    try {
      const body = (await res.json()) as { error?: string }
      if (body?.error) message = body.error
    } catch {
      // A refusal with no JSON body is still a refusal; the status stands in.
    }
    throw new Error(message)
  }
  return (await res.json()) as WirePageBundle
}

export interface ImportVariables {
  bundle: WirePageBundle
  /** Install under a different slug. Empty keeps the bundle's own. */
  slug?: string
  /** One entry per bindable reference: the bundle's `ref` → this workspace's. */
  bind: Record<string, string>
}

/**
 * POST — install a bundle as a page here.
 *
 * The failure worth handling is not a network error: it is a 422 naming EVERY
 * reference that could not be bound. Nothing is written in that case, so the
 * honest thing for a caller to do is show the list and let the person map them
 * — which is why `usePageImport` surfaces the parsed list rather than only a
 * sentence.
 */
export function usePageImport(
  workspaceId: string,
  cb: WriteCallbacks<{ slug: string }> & {
    onUnresolved?: (refs: WireUnresolvedRef[], message: string) => void
  } = {},
): UseApiMutationResult<ImportVariables, { slug?: string }> {
  return useApiMutation<ImportVariables, { slug?: string }>({
    request: (v) => ({
      input: `/api/v1/pages/import${pageQueryString(workspaceId)}`,
      init: {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          format: v.bundle.format,
          page: v.bundle.page,
          references: v.bundle.references,
          ...(v.slug ? { slug: v.slug } : {}),
          ...(Object.keys(v.bind).length > 0 ? { bind: v.bind } : {}),
        }),
      },
    }),
    invalidateKeys: [["pages", workspaceId]],
    onOk: (data) => cb.onOk?.({ slug: (data as { slug?: string })?.slug ?? "" }),
    onAlreadyRunning: (outcome) => cb.onRefused?.(outcome.message),
    onError: (err) => {
      // The 422 carries `unresolved`; ApiMutationError keeps the parsed body
      // when there is one, so a caller can render the list instead of a wall
      // of text naming references it cannot click on.
      const body = (err as { body?: { unresolved?: WireUnresolvedRef[] } })?.body
      const refs = body?.unresolved
      const message = pageWriteFailureMessage(err)
      if (Array.isArray(refs) && refs.length > 0) cb.onUnresolved?.(refs, message)
      else cb.onRefused?.(message)
    },
  })
}
