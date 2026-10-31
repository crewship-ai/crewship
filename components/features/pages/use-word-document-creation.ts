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

export type DocumentFormat = "native" | "sheet" | "wordcraft" | "doc" | "commands"

/**
 * What a confirmed creation hands back: the address, the fence a folder move
 * sends, and for a native Word or Sheet the Page identity its editor opens by.
 */
export interface CreatedDocument { slug: string; pagesVersion: number; nativePageId?: string }

interface Intent {
  name: string
  slug: string
  pageId?: string
  pagesVersion?: number
  attempted: boolean
  commandAttempted?: boolean
  nativeAttempted?: boolean
  nativeAdmission?: { token: string; generation: string }
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
  const selectedFormat: DocumentFormat = format ?? (availability.wordcraftNative ? "native" : availability.gridcraftNative === true ? "sheet" : availability.wordcraftSDKPrefix ? "wordcraft" : availability.data === true ? "doc" : availability.wordcraftCommands ? "commands" : "doc")
  // The Word tile never falls through to a Sheet, even when only GridCraft is native.
  const wordFormat: DocumentFormat = availability.wordcraftNative ? "native" : availability.wordcraftSDKPrefix ? "wordcraft" : availability.data === true ? "doc" : availability.wordcraftCommands ? "commands" : "doc"
  const wordFormatAvailable = wordFormat === "native" ? availability.wordcraftNative === true : wordFormat === "wordcraft" ? Boolean(availability.wordcraftSDKPrefix) : wordFormat === "commands" ? availability.wordcraftCommands === true : availability.data === true
  const formatAvailable = selectedFormat === "native" ? availability.wordcraftNative === true : selectedFormat === "sheet" ? availability.gridcraftNative === true : selectedFormat === "commands" ? availability.wordcraftCommands === true : selectedFormat === "wordcraft" ? Boolean(availability.wordcraftSDKPrefix) : availability.data === true
  const client = useQueryClient()
  const create = useMutation<CreatedDocument, Error, { name: string; slug: string; format?: DocumentFormat }>({ mutationFn: async input => {
    // An explicit format (Sheet from New artifact) is checked against its own
    // profile; otherwise the Word-first selection applies.
    const requested = input.format ?? selectedFormat
    const requestedAvailable = input.format === "sheet" ? availability.gridcraftNative === true : input.format === "native" ? availability.wordcraftNative === true : input.format === "wordcraft" ? Boolean(availability.wordcraftSDKPrefix) : input.format === "commands" ? availability.wordcraftCommands === true : input.format === "doc" ? availability.data === true : formatAvailable
    if (!allowed || !requestedAvailable) throw new Error("Document creation is unavailable.")
    const original = intent.current ??= { name: input.name.trim(), slug: input.slug, attempted: false, format: requested, sdkPrefix: availability.wordcraftSDKPrefix }
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
    if (original.format === "native" || original.format === "sheet") {
      const application = original.format === "sheet" ? "gridcraft" : "wordcraft"
      const base = `/api/v1/pages/${encodeURIComponent("@" + pageId)}/craft/`
      const query = new URLSearchParams({ workspace_id: workspaceId })
      if (!original.nativeAdmission) {
        if (original.nativeAttempted) throw new Error("Native initialization could not be confirmed. Check the original Page before starting again.")
        const bytes = application === "gridcraft"
          ? (await import("@/lib/mooring/gridcraft-empty")).emptyGridCraftNativeDocument()
          : (await import("@/lib/mooring/wordcraft-native-create")).createEmptyNativeWord()
        let binary = ""; for (let i = 0; i < bytes.length; i += 32768) binary += String.fromCharCode(...bytes.subarray(i, i + 32768))
        original.nativeAttempted = true
        const initialized = await apiFetch(`${base}initialize?${query}`, { method: "POST", redirect: "error", skipRefresh: true, headers: { "Content-Type": "application/json" }, body: JSON.stringify({ application_id: application, bytes: btoa(binary) }) })
        const value = await initialized.json().catch(() => null)
        if (!initialized.ok || value?.application_id !== application || value?.page_id !== pageId || value?.version !== "1" || typeof value?.session_token !== "string" || !/^[a-f0-9]{64}$/.test(value.session_token) || typeof value?.generation !== "string" || !/^[1-9][0-9]*$/.test(value.generation)) throw new Error("Native initialization could not be confirmed. Check the original Page before starting again.")
        original.nativeAdmission = { token: value.session_token, generation: value.generation }
      }
      // Creation does not keep a live editing connection. Release only the
      // exact initialized custody; the mounted editor admits its own session.
      let released = false
      try { released = (await apiFetch(`${base}lease/release?${query}`, { method: "POST", redirect: "error", skipRefresh: true, headers: { "Content-Type": "application/json", "X-Craft-Session": original.nativeAdmission.token }, body: JSON.stringify({ generation: original.nativeAdmission.generation }) })).ok } catch { /* The release may have committed before its response was lost. */ }
      if (!released) {
        // Initialization is already confirmed. A fresh authorized read may
        // open it safely; the editor starts read-only and never adopts this
        // creation admission or reacquires stale custody.
        const available = await apiFetch(`${base}availability?${query}`, { cache: "no-store", redirect: "error", skipRefresh: true })
        if (!available.ok || (await available.json()).enabled !== true) throw new Error("The created document could not be reopened. Check the original Page before starting again.")
      }
      return { ...done, nativePageId: pageId }
    }
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
    allowed, availability, selectedFormat, wordFormat, wordFormatAvailable, setFormat, formatAvailable, frozen, create,
    /** A Page exists and only its initialization is being retried. */
    retrying: Boolean(intent.current?.pageId),
  }
}
