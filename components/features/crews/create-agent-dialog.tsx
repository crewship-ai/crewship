"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"

const ROLES = [
  { value: "AGENT", label: "Agent" },
  { value: "LEAD", label: "Lead (1 per crew)" },
  { value: "COORDINATOR", label: "Coordinator (workspace-wide, no crew)" },
] as const

export interface CreateAgentDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  defaultCrewSlug: string | null
  crews: { id: string; name: string; slug: string }[]
  onCreated: (slug: string) => void
}

/**
 * Replaces the deleted /crews/agents/new full-page form. Minimal entry —
 * name + crew + role only — then redirects to the agent canvas where the
 * rest (system prompt, runtime, schedule, skills, credentials) is edited
 * inline.
 */
export function CreateAgentDialog({
  workspaceId,
  open,
  onOpenChange,
  defaultCrewSlug,
  crews,
  onCreated,
}: CreateAgentDialogProps) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [slug, setSlug] = useState("")
  const [crewSlug, setCrewSlug] = useState<string>(defaultCrewSlug ?? "")
  const [role, setRole] = useState<typeof ROLES[number]["value"]>("AGENT")
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (!open) {
      setName("")
      setSlug("")
      setCrewSlug(defaultCrewSlug ?? "")
      setRole("AGENT")
    } else {
      setCrewSlug(defaultCrewSlug ?? "")
    }
  }, [open, defaultCrewSlug])

  const [slugTouched, setSlugTouched] = useState(false)
  useEffect(() => {
    if (slugTouched) return
    setSlug(name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, ""))
  }, [name, slugTouched])

  // COORDINATOR has no crew. AGENT/LEAD require a crew.
  const requiresCrew = role !== "COORDINATOR"

  const submit = async () => {
    if (!name.trim() || !slug.trim()) return
    if (requiresCrew && !crewSlug) return
    setBusy(true)
    try {
      const targetCrew = requiresCrew ? crews.find((c) => c.slug === crewSlug) : null
      const body: Record<string, unknown> = {
        workspace_id: workspaceId,
        name: name.trim(),
        slug: slug.trim(),
        agent_role: role,
        crew_id: targetCrew?.id ?? null,
      }
      const res = await fetch("/api/v1/agents", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      const created = await res.json()
      toast.success(`Agent "${created.name}" created`)
      onOpenChange(false)
      onCreated(created.slug)
      router.replace(`/crews?agent=${encodeURIComponent(created.slug)}`)
    } catch (err) {
      toast.error(`Could not create agent: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setBusy(false)
    }
  }

  const valid =
    name.trim().length >= 2 &&
    /^[a-z0-9-]{2,}$/.test(slug) &&
    (!requiresCrew || crewSlug !== "")

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New agent</DialogTitle>
          <DialogDescription>
            Quick start. Edit system prompt, runtime, skills, and schedule on the agent canvas.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <label className="text-xs text-muted-foreground block">
            Name
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm outline-none focus:border-blue-400"
              placeholder="Filip"
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
              placeholder="filip"
            />
          </label>
          <label className="text-xs text-muted-foreground block">
            Role
            <select
              value={role}
              onChange={(e) => setRole(e.target.value as typeof role)}
              className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm outline-none focus:border-blue-400"
            >
              {ROLES.map((r) => (
                <option key={r.value} value={r.value} className="bg-zinc-900">
                  {r.label}
                </option>
              ))}
            </select>
          </label>
          {requiresCrew && (
            <label className="text-xs text-muted-foreground block">
              Crew
              <select
                value={crewSlug}
                onChange={(e) => setCrewSlug(e.target.value)}
                className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm outline-none focus:border-blue-400"
              >
                <option value="" disabled className="bg-zinc-900">
                  Pick a crew…
                </option>
                {crews.map((c) => (
                  <option key={c.id} value={c.slug} className="bg-zinc-900">
                    {c.name}
                  </option>
                ))}
              </select>
            </label>
          )}
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
            {busy ? "Creating…" : "Create agent"}
          </button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
