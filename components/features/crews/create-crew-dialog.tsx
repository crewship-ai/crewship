"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"

interface CrewTemplate {
  slug: string
  name: string
  description?: string | null
  icon?: string | null
  color?: string | null
}

export interface CreateCrewDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
}

/**
 * Replaces the deleted /crews/new full-page form with an inline modal.
 * Two paths: blank crew (just name + slug) or from a crew_templates row
 * (presets like "Research", "Engineering" with a default agent roster).
 */
export function CreateCrewDialog({ workspaceId, open, onOpenChange, onCreated }: CreateCrewDialogProps) {
  const router = useRouter()
  const [mode, setMode] = useState<"blank" | "template">("blank")
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [templates, setTemplates] = useState<CrewTemplate[]>([])
  const [pickedTemplate, setPickedTemplate] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // Reset state on close so a re-open starts clean.
  useEffect(() => {
    if (!open) {
      setMode("blank")
      setName("")
      setSlug("")
      setPickedTemplate(null)
    }
  }, [open])

  // Lazy-load templates on first open into template mode.
  useEffect(() => {
    if (mode !== "template" || templates.length > 0) return
    let cancelled = false
    fetch("/api/v1/crew-templates")
      .then((r) => (r.ok ? r.json() : []))
      .then((data) => {
        if (!cancelled && Array.isArray(data)) setTemplates(data)
      })
      .catch(() => { /* silent — empty list is fine fallback */ })
    return () => { cancelled = true }
  }, [mode, templates.length])

  // Auto-derive slug from name unless the user has manually edited it.
  const [slugTouched, setSlugTouched] = useState(false)
  useEffect(() => {
    if (slugTouched) return
    setSlug(name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""))
  }, [name, slugTouched])

  const submit = async () => {
    if (!name.trim() || !slug.trim()) return
    setBusy(true)
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        slug: slug.trim(),
      }
      if (mode === "template" && pickedTemplate) {
        body.template_slug = pickedTemplate
      }
      const res = await fetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      const created = await res.json()
      toast.success(`Crew "${created.name}" created`)
      onOpenChange(false)
      onCreated()
      router.replace(`/crews?crew=${encodeURIComponent(created.slug)}`)
    } catch (err) {
      toast.error(`Could not create crew: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(false)
    }
  }

  const valid = name.trim().length >= 2 && /^[a-z0-9-]{2,}$/.test(slug)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New crew</DialogTitle>
          <DialogDescription>
            Create a new crew. Blank starts empty; templates seed a typical role lineup.
          </DialogDescription>
        </DialogHeader>

        <div className="flex gap-2 mb-2">
          <button
            type="button"
            onClick={() => setMode("blank")}
            className={`flex-1 px-3 py-2 rounded border text-sm transition-colors ${
              mode === "blank"
                ? "border-blue-400 bg-blue-500/10 text-blue-300"
                : "border-white/10 hover:bg-white/5"
            }`}
          >
            Blank
          </button>
          <button
            type="button"
            onClick={() => setMode("template")}
            className={`flex-1 px-3 py-2 rounded border text-sm transition-colors ${
              mode === "template"
                ? "border-blue-400 bg-blue-500/10 text-blue-300"
                : "border-white/10 hover:bg-white/5"
            }`}
          >
            From template
          </button>
        </div>

        {mode === "template" && (
          <div className="max-h-[200px] overflow-y-auto rounded border border-white/10 divide-y divide-white/5 mb-2">
            {templates.length === 0 && (
              <div className="px-3 py-4 text-xs text-muted-foreground text-center">
                No templates available
              </div>
            )}
            {templates.map((t) => (
              <button
                key={t.slug}
                type="button"
                onClick={() => {
                  setPickedTemplate(t.slug)
                  if (!name) setName(t.name)
                }}
                className={`w-full px-3 py-2 text-left text-sm hover:bg-white/5 transition-colors ${
                  pickedTemplate === t.slug ? "bg-blue-500/10" : ""
                }`}
              >
                <div className="font-medium">{t.name}</div>
                {t.description && (
                  <div className="text-xs text-muted-foreground mt-0.5">{t.description}</div>
                )}
              </button>
            ))}
          </div>
        )}

        <div className="space-y-2">
          <label className="text-xs text-muted-foreground block">
            Name
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm outline-none focus:border-blue-400"
              placeholder="Engineering"
            />
          </label>
          <label className="text-xs text-muted-foreground block">
            Slug
            <input
              type="text"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value)
                setSlugTouched(true)
              }}
              className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm font-mono outline-none focus:border-blue-400"
              placeholder="engineering"
            />
          </label>
        </div>

        <DialogFooter>
          <button
            type="button"
            className="text-sm px-3 py-1.5 rounded text-muted-foreground hover:text-foreground"
            onClick={() => onOpenChange(false)}
            disabled={busy}
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={!valid || busy}
            className="text-sm px-3 py-1.5 rounded bg-blue-500 hover:bg-blue-400 text-white disabled:opacity-40"
          >
            {busy ? "Creating…" : "Create crew"}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
