"use client"

import { useState } from "react"
import Link from "next/link"
import { Archive, CheckCircle2, Sparkles } from "lucide-react"

import { toast } from "@/lib/toast"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { PageEmpty } from "@/components/layout/page-state"
import { entityHref } from "@/lib/entity-links"
import { SkillSourceMark, SkillTile } from "./skill-marks"
import { attentionReasons, skillName, type ProposedSkill, type SkillRow, type SkillTab, type SkillsCrew } from "./skills-model"

// The two views that are not the library table (Harbor 2 #3396): what needs a
// person, one row and one verb each, and what agents proposed.

export function AttentionView({ skills, onOpen }: { skills: SkillRow[]; onOpen: (id: string, tab?: SkillTab) => void }) {
  const items = skills.flatMap((s) => attentionReasons(s).map((r) => ({ s, r })))
  if (items.length === 0) {
    return <PageEmpty icon={CheckCircle2} title="Nothing needs attention" description="Flagged skills, skills that were never scanned and agents missing a credential a skill needs show up here." />
  }
  return (
    <section className="overflow-hidden rounded-card border border-border bg-card" aria-label={`Needs attention · ${items.length}`} data-slot="skills-attention">
      <header className="flex items-center gap-2 px-4 py-3">
        <h2 className="text-control font-semibold">
          Needs attention <span className="font-normal text-muted-foreground">· {items.length}</span>
        </h2>
      </header>
      <ul>
        {items.map(({ s, r }) => (
          <li key={`${s.id}:${r.kind}`} className="flex min-h-12 items-center gap-3 border-t border-border px-4 py-2.5">
            <SkillSourceMark skill={s} size={28} />
            <div className="min-w-0 flex-1">
              <div className="truncate text-control font-medium">
                {skillName(s)} · {r.title}
              </div>
              <div className="truncate text-label text-muted-foreground">{r.detail}</div>
            </div>
            {r.kind === "credential" ? (
              <Button asChild size="sm">
                <Link href={entityHref({ kind: "credentials" })}>Assign</Link>
              </Button>
            ) : (
              <Button size="sm" onClick={() => onOpen(s.id, "security")}>
                Review
              </Button>
            )}
          </li>
        ))}
      </ul>
    </section>
  )
}

export function ProposedView({
  proposed,
  crews,
  busy,
  onReview,
}: {
  proposed: ProposedSkill[]
  crews: SkillsCrew[]
  busy: boolean
  onReview: (p: ProposedSkill, approve: boolean) => Promise<void>
}) {
  const [rejecting, setRejecting] = useState<ProposedSkill | null>(null)
  if (proposed.length === 0) {
    return (
      <PageEmpty
        icon={Sparkles}
        title="Nothing proposed"
        description="Agents propose a skill when a crew's memory shows the same steps again and again, or when an agent writes one."
        cli="crewship skill proposed list --crew <crew>"
      />
    )
  }
  return (
    <div className="flex flex-col gap-3" data-slot="skills-proposed">
      {proposed.map((p) => {
        const crew = crews.find((c) => c.id === p.crew_id)
        return (
          <section key={`${p.crew_id}/${p.file_name}`} className="overflow-hidden rounded-card border border-border bg-card" aria-label={p.name}>
            <header className="flex flex-wrap items-center gap-2.5 px-4 pt-3">
              <SkillTile category={p.category} size="sm" />
              <h3 className="text-control font-semibold">{p.name}</h3>
              <StatusPill className="font-sans tracking-normal" tone="purple" label="Proposed" />
              {p.description_quality && p.description_quality !== "OK" && <StatusPill className="font-sans tracking-normal" tone="warn" label="Weak trigger" />}
            </header>
            <div className="flex flex-col gap-2 px-4 py-2.5">
              <p className="text-control text-foreground/85">{p.description}</p>
              {crew && (
                <div className="flex items-center gap-1.5 text-label text-muted-foreground">
                  <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-4 w-4 rounded-sm [&>svg]:h-2.5 [&>svg]:w-2.5" />
                  Proposed in {crew.name} · <span className="font-mono">{p.file_name}</span>
                </div>
              )}
            </div>
            <div className="flex gap-2 border-t border-border px-4 py-2.5">
              <Button size="sm" disabled={busy} onClick={() => void onReview(p, true).catch(() => undefined)}>
                Approve and add
              </Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setRejecting(p)}>
                Reject
              </Button>
            </div>
          </section>
        )
      })}
      <p className="text-label text-muted-foreground-soft">Approving adds the skill to the library; give it to agents from its page.</p>
      <ConfirmDialog
        open={rejecting !== null}
        onOpenChange={(o) => !o && setRejecting(null)}
        title={`Reject ${rejecting?.name ?? "this proposal"}?`}
        description="The staged draft is deleted from the crew's memory folder."
        consequences={[
          { tone: "lost", text: "The proposed SKILL.md is removed" },
          { tone: "kept", text: "The library and every agent's skills stay as they are" },
        ]}
        confirmLabel="Reject"
        destructive
        onConfirm={async () => {
          if (rejecting) await onReview(rejecting, false)
        }}
      />
    </div>
  )
}

export function ArchivedBanner() {
  return (
    <div className="page-banner" role="note" data-slot="skills-archived-banner">
      <Archive className="size-4 shrink-0" aria-hidden />
      <span>
        <b>Archived after 90 days without use.</b> Hidden from the library and from agent pickers; agents that still have one keep it.
      </span>
    </div>
  )
}

export function reviewToast(p: ProposedSkill, approve: boolean, err?: unknown) {
  if (err) toast.error(err instanceof Error ? err.message : "Could not review the proposal")
  else toast.success(approve ? `${p.name} added to the library · give it to agents to use it` : `${p.name} rejected`)
}
