"use client"

import { useRef, useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { pagesKeys } from "@/hooks/use-pages"
import { documentRequest } from "@/hooks/use-page-document"
import { useDocumentsAvailability } from "@/hooks/use-documents-availability"
import { usePageCreationAccess } from "@/hooks/use-page-creation-access"
import { createEmptyWordCraftCheckpoint } from "@/lib/mooring/wordcraft-create"
import { openWordCraftSession } from "@/lib/mooring/wordcraft-host-session"
import { commandRequest } from "@/lib/mooring/wordcraft-command-session"
import { verifyInitialization } from "@/lib/documents/contracts"

export type DocumentFormat = "wordcraft" | "doc" | "commands"

/** What a confirmed creation hands back: the address, and the fence a folder move sends. */
export interface CreatedDocument { slug: string; pagesVersion: number }

interface Intent {
  name: string
  slug: string
  pageId?: string
  pagesVersion?: number
  attempted: boolean
  commandAttempted?: boolean
  format: DocumentFormat
  sdkPrefix?: string
  checkpoint?: string
  initialize?: { id: string; schema: 3; page_id: string; body: object }
}

/**
 * Word document creation, shared by New Doc and New artifact so both keep the
 * same retry rules: the first attempt freezes one intent (name, address,
 * format) and every retry continues THAT Page, never a second one.
 */
export function useWordDocumentCreation(workspaceId: string) {
  const [frozen, setFrozen] = useState(false)
  const [format, setFormat] = useState<DocumentFormat>()
  const intent = useRef<Intent>(undefined)
  const allowed = usePageCreationAccess(workspaceId)
  const availability = useDocumentsAvailability(workspaceId)
  const selectedFormat: DocumentFormat = format ?? (availability.wordcraftSDKPrefix ? "wordcraft" : availability.data === true ? "doc" : availability.wordcraftCommands ? "commands" : "doc")
  const formatAvailable = selectedFormat === "commands" ? availability.wordcraftCommands === true : selectedFormat === "wordcraft" ? Boolean(availability.wordcraftSDKPrefix) : availability.data === true
  const client = useQueryClient()
  const create = useMutation<CreatedDocument, Error, { name: string; slug: string }>({ mutationFn: async input => {
    if (!allowed || !formatAvailable) throw new Error("Document creation is unavailable.")
    const original = intent.current ??= { name: input.name.trim(), slug: input.slug, attempted: false, format: selectedFormat, sdkPrefix: availability.wordcraftSDKPrefix }
    setFrozen(true)
    if (!original.pageId) {
      // Page creation has no idempotency key. An ambiguous response must not
      // create a second Page or adopt an existing Page by its address.
      if (original.attempted) throw new Error("Page creation could not be confirmed. Check the Pages list before starting again.")
      original.attempted = true
      const response = await apiFetch(`/api/v1/pages?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: original.name, slug: original.slug, panels: [], allow_empty: true }) })
      const value = await response.json().catch(() => null)
      if (!response.ok) {
        if (response.status >= 400 && response.status < 500 && ![408, 429].includes(response.status)) { intent.current = undefined; setFrozen(false) }
        throw new Error(value?.error ?? "Page creation could not be confirmed.")
      }
      if (typeof value?.id !== "string" || !value.id) throw new Error("Page creation did not confirm its stable identity. Check the Pages list before starting again.")
      original.pageId = value.id
      // A Page created this moment has never moved; 0 is the column default.
      original.pagesVersion = typeof value?.pages_version === "number" ? value.pages_version : 0
    }
    const pageId = original.pageId
    if (!pageId) throw new Error("The original Page identity is unavailable.")
    const done = { slug: original.slug, pagesVersion: original.pagesVersion ?? 0 }
    if (original.format === "commands") {
      // Only this just-created empty Page is eligible. On an ambiguous create
      // response, a user retry READS/restores the same original Page; it never
      // repeats initialization or converts an existing pilot document.
      const initialize = !original.commandAttempted
      original.commandAttempted = true
      const opened = await commandRequest(workspaceId, pageId, { action: "open", write: true, ...(initialize ? { create: true } : {}) }, new AbortController().signal)
      if (opened.outcome !== null || opened.projection.revision !== 0) throw new Error("The original empty command document could not be confirmed.")
      return done
    }
    if (original.format === "wordcraft") {
      if (!original.sdkPrefix) throw new Error("WordCraft creation is unavailable.")
      original.checkpoint ??= await createEmptyWordCraftCheckpoint(original.sdkPrefix)
      // A failed or ambiguous response keeps the SAME Page/checkpoint for retry;
      // the host's initialize path never replaces an existing native document.
      await openWordCraftSession(workspaceId, pageId, true, new AbortController().signal, original.checkpoint)
      return done
    }
    const input2 = original.initialize ??= { id: crypto.randomUUID(), schema: 3, page_id: pageId, body: { type: "doc", content: [{ type: "paragraph", attrs: { id: crypto.randomUUID() } }] } }
    const acknowledged = await documentRequest(workspaceId, original.slug, "/initialize", input2)
    verifyInitialization(acknowledged, input2.id)
    // A numeric initialization fence is not an editor state. Opening the Page
    // performs a fresh authorized read with the opaque public epoch.
    return done
  }, onSuccess: async () => {
    await client.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
  } })
  return {
    allowed, availability, selectedFormat, setFormat, formatAvailable, frozen, create,
    /** A Page exists and only its initialization is being retried. */
    retrying: Boolean(intent.current?.pageId),
  }
}
