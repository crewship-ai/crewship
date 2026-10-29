"use client"

import { useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Eye, Plus, Search, TriangleAlert } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Segmented, type SegmentedOption } from "@/components/ui/segmented"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"
import { pagesKeys, usePages, type PageView } from "@/hooks/use-pages"
import { usePageFolderMutations, usePageFolders, type PageFolderView } from "@/hooks/use-page-folders"
import { createWorkbook } from "@/lib/workbooks/create"
import { useWordDocumentCreation } from "@/components/features/pages/use-word-document-creation"
import {
  artifactKindOfPage,
  comingKinds,
  creatableKinds,
  type ArtifactKindEntry,
  type ArtifactKindKey,
} from "@/components/features/chat/artifact-context/new-artifact/artifact-kinds"
import { useArtifactCapabilities } from "@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities"

type Start = "blank" | "tmpl" | "upload" | "describe"

/** The search appears once the registry outgrows one screen of thumbnails. */
const SEARCH_FROM = 9

/**
 * Start options per kind. Only what works today is enabled: Crafts have no
 * template or import route in the browser yet, so those say "Coming".
 */
function startOptions(kind: ArtifactKindEntry): SegmentedOption<Start>[] {
  if (kind.make === "agent") return [{ value: "describe", label: "Describe it" }, { value: "tmpl", label: "From template" }]
  const tmpl: SegmentedOption<Start> = { value: "tmpl", label: "From template", disabled: true, title: "Coming" }
  if (kind.make === "panels") return [{ value: "blank", label: "Empty page" }, tmpl]
  return [
    { value: "blank", label: "Blank" },
    tmpl,
    { value: "upload", label: "Upload", disabled: true, title: `Coming: ${kind.uploadExtensions.join(" or ")}` },
    ...(kind.drafted ? [{ value: "describe" as const, label: "Describe it" }] : []),
  ]
}

const slugify = (name: string) => name.toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 60).replace(/-$/, "")

/** An address no Page in the cached list uses; the server still refuses a clash. */
function freeSlug(name: string, fallback: string, pages: readonly PageView[]): string {
  const base = slugify(name) || fallback
  const taken = new Set(pages.map(p => p.slug))
  let slug = base
  for (let n = 2; taken.has(slug); n++) slug = `${base}-${n}`
  return slug
}

/** The chat draft an agent starts from. The person reads and sends it. */
function agentPrompt(kind: ArtifactKindEntry, start: Start, name: string, prompt: string, template: string): string {
  const noun = (kind.newLabel ?? kind.label).toLowerCase()
  const named = name ? ` "${name}"` : ""
  if (start === "tmpl") return `Create a ${noun} from the ${template} template${name ? ` named "${name}"` : ""}`
  return prompt ? `Create a ${noun}${named}: ${prompt}` : `Create a ${noun}${named}`
}

interface Created { slug: string; pagesVersion: number }

export interface NewArtifactDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  defaultFolderSlug?: string
  defaultKind?: ArtifactKindKey
  /** Opens the artifact: the new one, or an existing one the duplicate warning points at. */
  onCreated: (slug: string) => void
}

export function NewArtifactDialog(props: NewArtifactDialogProps) {
  // Mounted only while open, so every opening starts from the defaults.
  return props.open ? <NewArtifactForm {...props} /> : null
}

function NewArtifactForm({ workspaceId, onOpenChange, defaultFolderSlug, defaultKind, onCreated }: NewArtifactDialogProps) {
  const router = useRouter()
  const client = useQueryClient()
  const caps = useArtifactCapabilities(workspaceId)
  const creatable = creatableKinds(caps)
  const coming = comingKinds(caps)
  const { pages } = usePages(workspaceId)
  const { folders } = usePageFolders(workspaceId)
  const { addPage } = usePageFolderMutations(workspaceId)
  const word = useWordDocumentCreation(workspaceId)

  const [query, setQuery] = useState("")
  const [picked, setPicked] = useState<ArtifactKindKey | undefined>(defaultKind)
  const [name, setName] = useState("")
  const [prompt, setPrompt] = useState("")
  const [template, setTemplate] = useState<string>()
  const [startPick, setStartPick] = useState<Start>()
  // Only a folder the caller may file into; one marked otherwise starts unfiled.
  const [folderPick, setFolderPick] = useState(() => folders.find(f => f.slug === defaultFolderSlug && f.canAcceptPages !== false)?.slug)
  const [created, setCreated] = useState<{ slug: string; name: string; folder: string; error: string } | null>(null)
  // A Page create has no idempotency key; an unconfirmed one is never repeated.
  const pageIntent = useRef<{ slug: string; created?: Created; uncertain?: boolean }>(undefined)

  const kind = creatable.find(k => k.key === picked) ?? creatable[0]
  const starts = kind ? startOptions(kind) : []
  const start = starts.find(o => o.value === startPick && !o.disabled)?.value ?? starts[0]?.value ?? "blank"
  const handoff = start === "describe" || (kind?.make === "agent" && start === "tmpl")
  const chosenTemplate = template && kind?.templates.includes(template) ? template : kind?.templates[0] ?? ""
  // A defaultFolderSlug may arrive before the folder list; keep it once the list has it.
  const folderSlug = folderPick ?? folders.find(f => f.slug === defaultFolderSlug && f.canAcceptPages !== false)?.slug ?? ""
  const folder: PageFolderView | undefined = handoff ? undefined : folders.find(f => f.slug === folderSlug)
  const trimmed = name.trim()

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return creatable
    return creatable.filter(k => [k.label, k.newLabel ?? "", k.description, ...k.templates].join(" ").toLowerCase().includes(q))
  }, [creatable, query])

  const duplicate = trimmed && kind ? pages.find(p => artifactKindOfPage(p) === kind.key && (p.folder?.slug ?? "") === (folder?.slug ?? "") && p.name.trim().toLowerCase() === trimmed.toLowerCase()) : undefined

  const create = useMutation<Created, Error, ArtifactKindEntry>({ mutationFn: async k => {
    if (k.key === "word") return word.create.mutateAsync({ name: trimmed, slug: freeSlug(trimmed, "document", pages) })
    if (k.key === "sheet") {
      const made = await createWorkbook(workspaceId, { name: trimmed, slug: freeSlug(trimmed, "sheet", pages) })
      await client.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
      // A new workbook Page has never moved; 0 is the column default.
      return { slug: made.slug, pagesVersion: 0 }
    }
    const intent = pageIntent.current ??= { slug: freeSlug(trimmed, "page", pages) }
    if (intent.created) return intent.created
    if (intent.uncertain) throw new Error("Page creation could not be confirmed. Check the library before starting again.")
    intent.uncertain = true
    const response = await apiFetch(`/api/v1/pages?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: trimmed, slug: intent.slug, panels: [], allow_empty: true }) })
    const value = await response.json().catch(() => null)
    if (!response.ok) {
      // A clear refusal created nothing, so the person may change the name and try again.
      if (response.status >= 400 && response.status < 500 && ![408, 429].includes(response.status)) pageIntent.current = undefined
      throw new Error(value?.error ?? "Page creation could not be confirmed.")
    }
    intent.uncertain = false
    intent.created = { slug: typeof value?.slug === "string" ? value.slug : intent.slug, pagesVersion: typeof value?.pages_version === "number" ? value.pages_version : 0 }
    await client.invalidateQueries({ queryKey: pagesKeys.all(workspaceId) })
    return intent.created
  } })

  const busy = create.isPending
  const frozen = busy || word.frozen || Boolean(pageIntent.current) || Boolean(created)
  const close = () => { if (!busy) onOpenChange(false) }
  const open = (slug: string) => { onCreated(slug); onOpenChange(false) }

  const submit = async () => {
    if (!kind || busy) return
    if (created) return open(created.slug)
    if (handoff) {
      const text = agentPrompt(kind, start, trimmed, prompt.trim(), chosenTemplate)
      // The composer's own creation switch carries the intent; this only fills the draft.
      router.push(`/chat?${new URLSearchParams({ prompt: text, draft: "1" })}`)
      onOpenChange(false)
      return
    }
    let made: Created
    try { made = await create.mutateAsync(kind) } catch { return }
    if (folder) {
      try {
        await addPage.mutateAsync({ folder: folder.slug, page: made.slug, pagesVersion: made.pagesVersion, aclVersion: folder.aclVersion })
      } catch (error) {
        setCreated({ slug: made.slug, name: trimmed, folder: folder.name, error: error instanceof Error ? error.message : "the folder refused it" })
        return
      }
    }
    open(made.slug)
  }

  const canSubmit = Boolean(kind) && !busy && (created ? true
    : handoff ? start === "tmpl" || Boolean(prompt.trim() || trimmed)
    : Boolean(trimmed) && (kind?.key !== "word" || (word.allowed && word.formatAvailable)))
  const primaryLabel = created ? "Open it" : handoff ? "Continue in chat" : busy ? "Creating…" : word.retrying && kind?.key === "word" ? "Retry initialization" : kind?.make === "panels" ? "Create page" : "Create and edit"
  const refusal = created ? `${created.name} was created but not put in ${created.folder}: ${created.error}` : create.error?.message ?? null

  return <CreateSurface open size="lg" onOpenChange={next => { if (!next) close() }} onSubmit={() => { if (canSubmit) void submit() }} ariaLabel="New artifact">
    <CreateSurfaceHeader icon={Plus} context={folder ? `in ${folder.name}` : "Artifacts"} title="New artifact" description="Pick what you are making. An artifact holds one kind of content." onClose={busy ? undefined : close} />
    <CreateSurfaceBody className="flex flex-col gap-3.5">
      {creatable.length >= SEARCH_FROM && <label className="relative block">
        <Search aria-hidden className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input type="search" aria-label="Find a kind or template" placeholder="Find a kind or template" className={cn(CREATE_SURFACE_INPUT, "pl-8")} value={query} onChange={e => setQuery(e.target.value)} />
      </label>}
      {creatable.length === 0
        ? <CreateSurfaceNotice tone="info">Nothing can be created in this workspace right now.</CreateSurfaceNotice>
        : <div role="group" aria-label="Kind" className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          {shown.map(k => <CreateSurfaceTile key={k.key} data-kind={k.key} icon={k.icon} accent={k.accent} title={k.newLabel ?? k.label} description={k.description}
            selected={k.key === kind?.key} disabled={frozen} onClick={() => { setPicked(k.key); setStartPick(undefined) }} />)}
        </div>}
      {coming.length > 0 && <p className="text-micro text-muted-foreground-soft">Coming: {coming.map(k => k.label).join(", ")}</p>}

      {kind && <>
        <div className="grid gap-3 sm:grid-cols-2">
          <CreateSurfaceField label={handoff ? <>Name <span className="font-normal text-muted-foreground-soft">· optional</span></> : "Name"} htmlFor="new-artifact-name">
            <Input id="new-artifact-name" className={CREATE_SURFACE_INPUT} autoFocus autoComplete="off" disabled={frozen} value={name}
              placeholder={start === "describe" ? "The agent names it" : start === "tmpl" ? chosenTemplate : kind.placeholder} onChange={e => setName(e.target.value)} />
          </CreateSurfaceField>
          <CreateSurfaceField label="Folder" htmlFor="new-artifact-folder" hint={handoff ? "The agent creates it unfiled; file it after." : undefined}>
            <select id="new-artifact-folder" className={cn(CREATE_SURFACE_INPUT, "rounded-md border bg-transparent px-2")} disabled={frozen || handoff} value={handoff ? "" : folderSlug} onChange={e => setFolderPick(e.target.value)}>
              <option value="">Unfiled</option>
              {folders.map(f => <option key={f.slug} value={f.slug} disabled={f.canAcceptPages === false}>{f.name}{f.canAcceptPages === false ? " · view only" : ""}</option>)}
            </select>
          </CreateSurfaceField>
        </div>

        {duplicate && <div className="flex items-start gap-2 rounded-md border border-warn/30 bg-warn/[0.07] p-2.5 text-xs">
          <TriangleAlert aria-hidden className="mt-px size-3.5 shrink-0 text-warn" />
          <p className="min-w-0 flex-1"><b className="font-semibold">{duplicate.name}</b> already exists here. A second one with the same name is hard to tell apart.</p>
          <Button type="button" size="sm" variant="secondary" className="h-7 text-xs" onClick={() => open(duplicate.slug)}>Open it</Button>
        </div>}

        <p className="flex items-center gap-2 text-xs text-muted-foreground">
          <Eye aria-hidden className="size-3.5 shrink-0" />
          <span>Who can open: <b className="font-medium text-foreground">{folder ? `People with access to ${folder.name}` : "Only you until you share it"}</b></span>
          <span className="ml-auto text-micro text-muted-foreground-soft max-sm:hidden">Change in Share after</span>
        </p>

        <div className="flex flex-col gap-1.5">
          <span className="text-xs font-semibold text-muted-foreground">Start with</span>
          <Segmented ariaLabel="Start with" value={start} options={frozen ? starts.map(o => ({ ...o, disabled: true })) : starts} onChange={setStartPick} />
        </div>

        {start === "describe" && <CreateSurfaceField label={kind.make === "edit" ? "What should the first draft contain?" : "What should it contain?"} htmlFor="new-artifact-prompt"
          hint={`Opens chat with this as a draft. Send it with ${kind.make === "edit" ? "Create document" : "Create artifact project"} on.`}>
          <Textarea id="new-artifact-prompt" rows={3} value={prompt} placeholder={kind.starterPrompts[0]} onChange={e => setPrompt(e.target.value)} />
        </CreateSurfaceField>}
        {start === "tmpl" && <div role="group" aria-label="Templates" className="flex flex-wrap gap-1.5">
          {kind.templates.map(t => <button key={t} type="button" aria-pressed={t === chosenTemplate} onClick={() => setTemplate(t)}
            className={cn("rounded-card border px-2.5 py-1.5 text-xs hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring coarse:min-h-[44px]", t === chosenTemplate && "border-transparent ring-2 ring-primary")}>{t}</button>)}
        </div>}
        {start === "blank" && <p className="text-micro text-muted-foreground-soft">{kind.make === "panels"
          ? "An empty Page. Crews add panels on their next run."
          : `Opens with you holding the lock. Nobody else sees it until you share a version.`}</p>}
      </>}
    </CreateSurfaceBody>
    <CreateSurfaceRefusal message={refusal} />
    <CreateSurfaceFooter
      hint={kind?.make === "edit" ? "One editor at a time · versions" : kind?.make === "panels" ? "Panels · no versions" : "Versions · comments for the agent"}
      onCancel={busy ? undefined : close} guardCancel={false}
      primaryLabel={primaryLabel} primaryDisabled={!canSubmit} busy={busy} onPrimary={() => void submit()} />
  </CreateSurface>
}
