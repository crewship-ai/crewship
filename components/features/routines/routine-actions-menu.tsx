"use client"

// The routine's ⋯ menu (Harbor 2): Duplicate as draft, Export bundle, Copy
// CLI command, Copy link, then Turn off / on and Delete routine for Owner and
// Admin. Every item does something the API performs; the two that cannot be
// taken back go through the shared confirmation dialog.
//
// Duplicate saves a DRAFT under a new ID, never a live routine: the copy is
// read, tested and published like any other change (the old Copy posted to
// /pipelines/save and put an untested copy live).

import * as React from "react"
import { Copy, Download, Link2, MoreHorizontal, Power, PowerOff, SquareTerminal, Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"

import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceRefusal,
} from "@/components/layout/create-surface"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { extractProblemDetail } from "@/lib/problem-details"
import { routinePermissions } from "@/lib/routine-governance"
import { saveRoutineDraft, RoutineDraftError } from "@/lib/routine-drafts"
import { SLUG_RE, slugFromName } from "@/lib/routine-wizard"
import type { RoutineDetail } from "./routines-detail-panel"

async function problemMessage(res: Response, fallback: string): Promise<string> {
  try {
    const body = await res.clone().json()
    return extractProblemDetail(body) ?? body?.error ?? fallback
  } catch {
    return fallback
  }
}

/** The draft document a duplicate starts as: the definition under a new ID. */
export function duplicateDraftDocument(
  routine: Pick<RoutineDetail, "description" | "definition" | "author_crew_id" | "icon" | "color">,
  next: { slug: string; name: string },
): Record<string, unknown> {
  const name = next.name.trim()
  return {
    slug: next.slug,
    name,
    description: routine.description ?? "",
    definition: { ...routine.definition, name: next.slug, display_name: name },
    ...(routine.author_crew_id ? { author_crew_id: routine.author_crew_id } : {}),
    ...(routine.icon ? { icon: routine.icon } : {}),
    ...(routine.color ? { color: routine.color } : {}),
  }
}

interface Props {
  routine: RoutineDetail
  workspaceId: string
  onChanged: () => void
  /** The routine is gone: leave its page. */
  onDeleted?: () => void
  /** Opens the copy's page once it is saved. */
  onOpenRoutine?: (slug: string) => void
  lifecycle?: "active" | "proposed" | "disabled"
  onGovernance?: (action: "disable" | "enable") => void
  governanceBusy?: boolean
  /** Ask from outside (Settings › Turn off or delete). */
  requestDelete?: number
}

export function RoutineActionsMenu({
  routine,
  workspaceId,
  onChanged,
  onDeleted,
  onOpenRoutine,
  lifecycle = "active",
  onGovernance,
  governanceBusy,
  requestDelete,
}: Props) {
  const { role, capabilities } = useAbilities()
  const permissions = routinePermissions(role, capabilities)
  const [dialog, setDialog] = React.useState<"duplicate" | "delete" | null>(null)
  const [busy, setBusy] = React.useState(false)
  const [refusal, setRefusal] = React.useState<string | null>(null)
  const [copyName, setCopyName] = React.useState("")
  const [copySlug, setCopySlug] = React.useState("")
  const [slugEdited, setSlugEdited] = React.useState(false)
  const displayName = routine.name || routine.slug
  const published = (routine.head_version ?? 0) > 0 && !routine.draft_only

  React.useEffect(() => {
    if (requestDelete) setDialog("delete")
  }, [requestDelete])

  const openDuplicate = () => {
    const name = `${displayName} (copy)`
    setCopyName(name)
    setCopySlug(slugFromName(name))
    setSlugEdited(false)
    setRefusal(null)
    setDialog("duplicate")
  }

  const slugOk = SLUG_RE.test(copySlug) && copySlug !== routine.slug

  const submitDuplicate = async () => {
    if (busy || !copyName.trim() || !slugOk) return
    setBusy(true)
    setRefusal(null)
    try {
      await saveRoutineDraft(
        workspaceId,
        { id: "", slug: copySlug, revision: 0, base_pipeline_id: "", base_revision: 0, document: {} },
        duplicateDraftDocument(routine, { slug: copySlug, name: copyName }),
      )
      toast.success(`${copyName.trim()} is saved as a draft · not live`, {
        description: "Schedules, webhooks and run history stay with the original.",
      })
      setDialog(null)
      onChanged()
      onOpenRoutine?.(copySlug)
    } catch (e) {
      setRefusal(
        e instanceof RoutineDraftError && e.status === 409
          ? `A routine or draft called “${copySlug}” already exists. Pick another ID.`
          : e instanceof Error
            ? e.message
            : String(e),
      )
    } finally {
      setBusy(false)
    }
  }

  const exportBundle = async () => {
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(routine.slug)}/export`,
      )
      if (!res.ok) {
        toast.error(await problemMessage(res, "Export failed"))
        return
      }
      const text = await res.text()
      // A bundle is a file you hand to `crewship routine import` or check
      // into a repo, not something to keep on the clipboard.
      const url = URL.createObjectURL(new Blob([text], { type: "application/json" }))
      const a = document.createElement("a")
      a.href = url
      a.download = `${routine.slug}.json`
      a.click()
      URL.revokeObjectURL(url)
      toast.success("Bundle exported")
    } catch {
      toast.error("Export failed")
    }
  }

  const copy = (text: string, done: string) => {
    void navigator.clipboard?.writeText(text).then(
      () => toast.success(done),
      () => toast.error("Could not copy"),
    )
  }

  const remove = async () => {
    const res = await apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(routine.slug)}`,
      { method: "DELETE" },
    )
    if (!res.ok) {
      toast.error("Could not delete the routine", { description: await problemMessage(res, `The server refused (${res.status}).`) })
      throw new Error("delete refused")
    }
    toast.success(`${displayName} deleted`, { description: "Its run history stays readable in Activity." })
    onChanged()
    onDeleted?.()
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button type="button" variant="ghost" size="icon-sm" aria-label="Routine options">
            <MoreHorizontal className="h-3.5 w-3.5" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60">
          <DropdownMenuLabel className="truncate text-label text-muted-foreground">{displayName}</DropdownMenuLabel>
          {permissions.author && (
            <DropdownMenuItem onSelect={openDuplicate}>
              <Copy className="h-3.5 w-3.5" />
              Duplicate as draft
            </DropdownMenuItem>
          )}
          {published && (
            <DropdownMenuItem onSelect={() => void exportBundle()}>
              <Download className="h-3.5 w-3.5" />
              Export bundle (JSON)
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => copy(`crewship routine run ${routine.slug}`, "CLI command copied")}>
            <SquareTerminal className="h-3.5 w-3.5" />
            Copy CLI command
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={() => copy(`${window.location.origin}/routines?${new URLSearchParams({ slug: routine.slug })}`, "Link copied")}
          >
            <Link2 className="h-3.5 w-3.5" />
            Copy link
          </DropdownMenuItem>
          {permissions.manage && published && (
            <>
              <DropdownMenuSeparator />
              {lifecycle !== "proposed" && onGovernance &&
                (lifecycle === "disabled" ? (
                  <DropdownMenuItem onSelect={() => onGovernance("enable")} disabled={governanceBusy}>
                    <Power className="h-3.5 w-3.5" />
                    Turn on
                  </DropdownMenuItem>
                ) : (
                  <DropdownMenuItem onSelect={() => onGovernance("disable")} disabled={governanceBusy}>
                    <PowerOff className="h-3.5 w-3.5" />
                    Turn off
                  </DropdownMenuItem>
                ))}
              <DropdownMenuItem variant="destructive" onSelect={() => setDialog("delete")}>
                <Trash2 className="h-3.5 w-3.5" />
                Delete routine…
              </DropdownMenuItem>
            </>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <CreateSurface
        open={dialog === "duplicate"}
        onOpenChange={(o) => !o && setDialog(null)}
        size="sm"
        onSubmit={() => void submitDuplicate()}
      >
        <CreateSurfaceHeader
          concept="routines"
          context={`Routines › ${displayName}`}
          title="Duplicate as draft"
          description="Copies the steps, inputs and limits under a new ID as a draft. Nothing goes live until you publish it; schedules, webhooks and history stay with the original."
          onClose={() => setDialog(null)}
        />
        <CreateSurfaceBody className="flex flex-col gap-3">
          <CreateSurfaceField label="Name" htmlFor="routine-copy-name" required>
            <Input
              id="routine-copy-name"
              value={copyName}
              onChange={(e) => {
                setCopyName(e.target.value)
                if (!slugEdited) setCopySlug(slugFromName(e.target.value))
              }}
              className={CREATE_SURFACE_INPUT}
            />
          </CreateSurfaceField>
          <CreateSurfaceField
            label="ID"
            htmlFor="routine-copy-slug"
            hint={slugOk ? "Lowercase letters, digits, - and _. Fixed after the first save." : copySlug === routine.slug ? "The copy needs an ID of its own." : "Start with a letter or digit; use lowercase letters, digits, - and _."}
          >
            <Input
              id="routine-copy-slug"
              value={copySlug}
              aria-invalid={!slugOk || undefined}
              onChange={(e) => {
                setSlugEdited(true)
                setCopySlug(e.target.value.trim())
              }}
              className={`${CREATE_SURFACE_INPUT} font-mono`}
            />
          </CreateSurfaceField>
        </CreateSurfaceBody>
        <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />
        <CreateSurfaceFooter
          onCancel={() => setDialog(null)}
          guardCancel={false}
          primaryLabel={busy ? "Saving…" : "Save as draft"}
          onPrimary={() => void submitDuplicate()}
          primaryDisabled={!copyName.trim() || !slugOk}
          busy={busy}
          primaryIcon={Copy}
        />
      </CreateSurface>

      <ConfirmDialog
        open={dialog === "delete"}
        onOpenChange={(o) => !o && setDialog(null)}
        title={`Delete ${displayName}?`}
        description="The routine stops: its schedules and webhooks no longer start it, and it leaves the list."
        consequences={[
          { tone: "lost", text: "Schedules, webhooks and the chat command stop starting it." },
          { tone: "kept", text: "Runs already recorded stay readable in Activity." },
          { tone: "warn", text: "Runs in progress are not stopped; stop them first if they must not finish." },
        ]}
        confirmLabel="Delete routine"
        destructive
        typeToConfirm={routine.slug}
        onConfirm={remove}
      />
    </>
  )
}
