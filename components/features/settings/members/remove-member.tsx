"use client"

import { useState } from "react"
import { Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { Combobox } from "@/components/ui/combobox"
import { ConfirmDialog, type Consequence } from "@/components/ui/confirm-dialog"
import { nativeSelect } from "@/components/features/settings/shared"

import type { Member } from "./members-model"
import type { PageToHandOver, RemoveOutcome } from "./use-members"

type HandOver = Extract<RemoveOutcome, { ok: false }>["handOver"]

/** People who may take over every one of `pages`. */
function commonSuccessors(pages: PageToHandOver[]) {
  if (pages.length === 0) return []
  return pages[0].successors.filter((c) => pages.every((p) => p.successors.some((s) => s.userId === c.userId)))
}

const personText = (c: { name: string; email: string }) => c.name.trim() || c.email

/**
 * Remove a member, and say what it costs. The server removes someone only
 * when every private artifact they own has another owner; when it does not,
 * it answers 409 `page_ownership_transfer_required`. An OWNER's answer lists
 * those artifacts and who may take each over: the dialog turns into a
 * successor picker, hands them over (POST …/page-ownership/{id}) and tries
 * the removal again. For anyone else the server's answer carries no list and
 * says an Owner has to choose, so the dialog says that and what to do next.
 */
export function RemoveMember({ member, label, tryRemove, handOverPage, onRemoved }: {
  member: Member
  label: string
  tryRemove: (m: Member) => Promise<RemoveOutcome>
  handOverPage: (m: Member, pageId: string, successorUserId: string) => Promise<string | null>
  onRemoved: (m: Member) => void
}) {
  const [open, setOpen] = useState(false)
  const [handOver, setHandOver] = useState<HandOver>(undefined)
  // One successor for every artifact when someone can take them all; per
  // artifact (`picks`) when nobody can.
  const [all, setAll] = useState("")
  const [picks, setPicks] = useState<Record<string, string>>({})

  const pages = handOver?.pages ?? []
  const common = commonSuccessors(pages)
  const single = common.length > 0
  const ready = pages.length > 0 && (single ? Boolean(all) : pages.every((p) => picks[p.id]))
  const waitingForOwner = Boolean(handOver) && pages.length === 0

  const reset = () => { setHandOver(undefined); setAll(""); setPicks({}) }

  async function remove() {
    // The first press asks the server; the second, after a successor was
    // chosen, hands the artifacts over and asks again.
    if (handOver && pages.length > 0) {
      for (const p of pages) {
        const successor = single ? all : picks[p.id]
        const refusal = await handOverPage(member, p.id, successor)
        if (refusal) {
          toast.error(`Couldn’t hand over “${p.name}”`, { description: refusal })
          throw new Error(refusal)
        }
      }
    }
    const outcome = await tryRemove(member)
    if (outcome.ok) { reset(); onRemoved(member); return }
    if (outcome.handOver) { setAll(""); setPicks({}); setHandOver(outcome.handOver) }
    throw new Error("Not removed")
  }

  const consequences: Consequence[] = [
    { tone: "lost", text: "They lose this workspace and every crew, routine and artifact in it at once" },
    { tone: "warn", text: "Private artifacts they own need a new owner first; if there is none, nothing is removed" },
    { tone: "kept", text: "Adding them again starts from the role’s default abilities" },
  ]

  return (
    <>
      <Button variant="outline" size="sm" className="gap-1.5 border-destructive/40 px-2.5 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive coarse:h-[44px]" onClick={() => setOpen(true)}>
        <Trash2 className="h-3 w-3" aria-hidden />
        Remove member
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={(o) => { setOpen(o); if (!o) reset() }}
        title={`Remove ${label}?`}
        description={handOver
          ? (waitingForOwner ? handOver.message : `${label} owns ${pages.length} private ${pages.length === 1 ? "artifact" : "artifacts"}. Choose who takes ${pages.length === 1 ? "it" : "them"} over, then ${label} is removed.`)
          : `${member.user.email} loses access to this workspace.`}
        consequences={handOver ? [] : consequences}
        confirmLabel={handOver ? "Hand over and remove" : "Remove member"}
        confirmDisabled={Boolean(handOver) && !ready}
        destructive
        onConfirm={remove}
      >
        {waitingForOwner && (
          <p data-slot="remove-owner-needed" className="text-label text-muted-foreground">
            Ask an Owner to choose who takes over their private artifacts, then remove them. An Owner can also run{" "}
            <code className="font-mono text-micro text-foreground">crewship workspace page-ownership list</code> and{" "}
            <code className="font-mono text-micro text-foreground">… transfer</code>.
          </p>
        )}
        {pages.length > 0 && single && (
          <div className="space-y-2" data-slot="remove-successor">
            <span className="text-label font-medium">Who takes over their private artifacts</span>
            <Combobox modal ariaLabel="Who takes over their private artifacts" value={all || null} onChange={setAll} placeholder="Choose a person…" searchPlaceholder={`Search ${common.length} people…`}
              options={common.map((c) => ({ value: c.userId, label: personText(c), description: c.name.trim() ? c.email : undefined, keywords: [c.email] }))} />
            <p className="text-label text-muted-foreground">
              {pages.slice(0, 5).map((p) => p.name).join(", ")}{pages.length > 5 ? ` and ${pages.length - 5} more` : ""}
            </p>
          </div>
        )}
        {pages.length > 0 && !single && (
          <div className="max-h-64 space-y-2 overflow-y-auto" data-slot="remove-successors">
            <p className="text-label text-muted-foreground">Nobody can take over all of them, so choose for each.</p>
            {pages.map((p) => (
              <label key={p.id} className="flex flex-col gap-1 text-label">
                <span className="truncate font-medium">{p.name}</span>
                <select aria-label={`Who takes over ${p.name}`} className={nativeSelect} value={picks[p.id] ?? ""} onChange={(e) => setPicks((cur) => ({ ...cur, [p.id]: e.target.value }))}>
                  <option value="">{p.successors.length ? "Choose a person…" : "Nobody can take it over"}</option>
                  {p.successors.map((c) => <option key={c.userId} value={c.userId}>{personText(c)}</option>)}
                </select>
              </label>
            ))}
          </div>
        )}
      </ConfirmDialog>
    </>
  )
}
