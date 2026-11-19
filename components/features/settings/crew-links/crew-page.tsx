"use client"

import * as React from "react"
import Link from "next/link"
import { Activity, ArrowLeft, ArrowRight, ChevronDown, ChevronUp, Link2, Lock, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ShowMore } from "@/components/ui/show-more"
import { useShowMore } from "@/hooks/use-show-more"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"

import {
  LEVEL_ALLOWS,
  LEVEL_LABEL,
  describeLinkChange,
  entryCrews,
  levelSentence,
  partnersWithLevels,
  type Connection,
  type Crew,
  type CrewAgent,
  type Level,
  type LinkAuditEntry,
  type Partner,
} from "./crew-links-model"
import { CrewHead, CrewTile, agentsLine } from "./crew-profile"
import { InactiveTag, LEVEL_ICON, LevelLadder, LevelTag } from "./level-ladder"
import type { useCrewLinks } from "./use-crew-links"

type Links = ReturnType<typeof useCrewLinks>

const PARTNER_STEP = 8
const FEW = 3

function CrewName({ crew, dup }: { crew: Crew; dup: boolean }) {
  return (
    <span className="flex min-w-0 flex-col leading-tight">
      <b className="truncate text-control font-medium">{crew.name}</b>
      {dup && <span className="truncate font-mono text-micro text-muted-foreground">{crew.slug}</span>}
    </span>
  )
}

/** One direction under its sentence: who hands work to whom, and the ladder (or a pill for a reader). */
function Direction({ from, to, level, links, filesKnown, onChange, icon: Icon }: {
  from: Crew; to: Crew; level: Level; links: Links; filesKnown: boolean; onChange: (l: Level) => void; icon: typeof ArrowRight
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5" data-slot="crew-link-direction">
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <div className="min-w-0 flex-[1_1_14rem]">
        <div className="text-control font-medium">{from.name} hands work to {to.name}</div>
        <div className="text-label text-muted-foreground">{levelSentence(from.name, to.name, level)}</div>
      </div>
      {links.canManage ? (
        <LevelLadder ariaLabel={`${from.name} to ${to.name}`} value={level} onChange={onChange} disabled={links.pending} filesKnown={filesKnown} />
      ) : (
        <LevelTag level={level} />
      )}
    </div>
  )
}

function PartnerRow({ crew, partner, open, onToggle, always, dup, links, filesKnown, agents }: {
  crew: Crew; partner: Partner; open: boolean; onToggle: () => void; always: boolean; dup: boolean
  links: Links; filesKnown: boolean; agents: CrewAgent[] | undefined
}) {
  const { crew: other, levels } = partner
  const set = (side: "out" | "in") => (level: Level) => void links.applyLevels(crew, other, { out: levels.out, in: levels.in, [side]: level })
  if (!open) {
    return (
      <button type="button" onClick={onToggle} aria-expanded={false} aria-label={`${other.name}: ${LEVEL_LABEL[levels.out]} out, ${LEVEL_LABEL[levels.in]} in`}
        className="flex w-full flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-border px-4 py-2.5 text-left last:border-b-0 hover:bg-[var(--row-hover-bg)] coarse:min-h-[44px]">
        <CrewTile crew={other} />
        <span className="min-w-0 flex-1 basis-32"><CrewName crew={other} dup={dup} /></span>
        {levels.inactive && <InactiveTag />}
        <span className="inline-flex items-center gap-1.5" title={`${crew.name} hands work to ${other.name}`}><ArrowRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden /><LevelTag level={levels.out} /></span>
        <span className="inline-flex items-center gap-1.5" title={`${other.name} hands work to ${crew.name}`}><ArrowLeft className="h-3.5 w-3.5 text-muted-foreground" aria-hidden /><LevelTag level={levels.in} /></span>
        <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
      </button>
    )
  }
  return (
    <div className="border-b border-border last:border-b-0" data-slot="crew-link-pair">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-4 pt-2.5">
        <CrewTile crew={other} />
        <span className="min-w-0 flex-1"><CrewName crew={other} dup={dup} /></span>
        {levels.inactive && <InactiveTag />}
        <span className="text-label text-muted-foreground">{agentsLine(other, agents) ?? ""}</span>
        {links.canManage && (
          <Button variant="ghost" size="sm" disabled={links.pending} aria-label={`Unlink ${crew.name} and ${other.name}`} onClick={() => void links.setPair(crew, other, "none")}>Unlink</Button>
        )}
        {!always && <Button variant="ghost" size="icon-sm" aria-label={`Close ${other.name}`} onClick={onToggle}><ChevronUp className="h-3.5 w-3.5" /></Button>}
      </div>
      <Direction from={crew} to={other} level={levels.out} links={links} filesKnown={filesKnown} onChange={set("out")} icon={ArrowRight} />
      <Direction from={other} to={crew} level={levels.in} links={links} filesKnown={filesKnown} onChange={set("in")} icon={ArrowLeft} />
    </div>
  )
}

/** "What each level allows", on the crew page and under the lists: the one legend. */
export function LevelsCard() {
  return (
    <SettingsCard title="What each level allows" description="Each one includes the ones above it" icon={Link2}>
      {(["work", "view", "deliver"] as const).map((l) => (
        <SettingsRow key={l} label={LEVEL_LABEL[l]} description={LEVEL_ALLOWS[l]} lead={LEVEL_ICON[l]}><span /></SettingsRow>
      ))}
      <SettingsRow label="Never" description="A link does not grant shell, credentials or agents’ private files" lead={Lock}><span /></SettingsRow>
    </SettingsCard>
  )
}

const when = (iso: string) => {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleString("en", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
}

/** The audit rows that name this crew, in this page's words. */
function ChangesCard({ crew, crews, entries, failed }: { crew: Crew; crews: Crew[]; entries: LinkAuditEntry[] | null; failed: boolean }) {
  const names = React.useMemo(() => new Map(crews.map((c) => [c.id, c.name])), [crews])
  const mine = React.useMemo(() => {
    const out: { id: string; text: string; at: string; actor: string | null }[] = []
    for (const e of entries ?? []) {
      const pair = entryCrews(e)
      if (!pair || !pair.includes(crew.id)) continue
      const text = describeLinkChange(e, (id) => names.get(id) ?? "a deleted crew")
      if (text) out.push({ id: e.id, text, at: e.at, actor: e.actor })
    }
    return out
  }, [entries, crew.id, names])
  const paged = useShowMore(mine, { step: 10, resetKey: crew.id })
  return (
    <SettingsCard title="Changes to these links" description="Who changed what, newest first" icon={Activity}
      footer={<><span>From the audit log.</span><Link href="/settings/audit?audit_type=CREW_LINK" className="ml-auto font-medium text-primary-hover hover:underline">Open the audit log</Link></>}>
      {failed && <p className="px-4 py-3 text-label text-muted-foreground">The changes could not be read.</p>}
      {!failed && entries === null && <p className="px-4 py-3 text-label text-muted-foreground">Reading the audit log…</p>}
      {!failed && entries !== null && mine.length === 0 && <p className="px-4 py-3 text-label text-muted-foreground">No changes to {crew.name}’s links are on record.</p>}
      {paged.shown.map((e) => (
        <SettingsRow key={e.id} label={e.text} description={e.actor ? `by ${e.actor}` : undefined} lead={Activity}>
          <span className="font-mono text-label text-muted-foreground">{when(e.at)}</span>
        </SettingsRow>
      ))}
      <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} noun="changes" paged={paged.paged} />
    </SettingsCard>
  )
}

/**
 * One crew and everyone it works with. Each partner is one row (two level
 * pills); opening it gives the two ladders. Few partners are all open.
 */
export function CrewPage({ crew, crews, connections, dups, links, agentsByCrew, filesKnown, showChanges, changes, onLinkCrew }: {
  crew: Crew
  crews: Crew[]
  connections: Connection[]
  dups: Set<string>
  links: Links
  agentsByCrew: Map<string, CrewAgent[]> | null
  filesKnown: boolean
  /** ADMIN and up: the audit list refuses anyone else, so the card is not drawn. */
  showChanges: boolean
  changes: { entries: LinkAuditEntry[] | null; failed: boolean }
  onLinkCrew: (c: Crew) => void
}) {
  const agentsOf = (c: Crew) => agentsByCrew?.get(c.id) ?? (agentsByCrew ? [] : undefined)
  const partners = React.useMemo(() => partnersWithLevels(crew.id, crews, connections), [crew.id, crews, connections])
  const paged = useShowMore(partners, { step: PARTNER_STEP, resetKey: crew.id })
  const [openId, setOpenId] = React.useState<string | null>(null)
  const always = partners.length <= FEW
  return (
    <div className="space-y-4">
      <CrewHead crew={crew} agents={agentsOf(crew)} showSlug={dups.has(crew.name)} />
      <SettingsCard
        title="Works with"
        description={partners.length ? `${partners.length} ${partners.length === 1 ? "crew" : "crews"} · what it hands over, and what it receives` : `${crew.name} works alone`}
        icon={Link2}
        actions={links.canManage ? (
          <Button variant="outline" size="sm" onClick={() => onLinkCrew(crew)}><Plus className="mr-1.5 h-3.5 w-3.5" />Link a crew</Button>
        ) : undefined}
      >
        {partners.length === 0 && (
          <p className="px-4 py-3 text-label text-muted-foreground">
            No crew hands work to {crew.name}, and it hands work to nobody.{links.canManage ? " Link a crew to change that." : ""}
          </p>
        )}
        {paged.shown.map((p) => (
          <PartnerRow key={p.conn.id} crew={crew} partner={p} always={always} open={always || openId === p.conn.id}
            onToggle={() => setOpenId((id) => (id === p.conn.id ? null : p.conn.id))}
            dup={dups.has(p.crew.name)} links={links} filesKnown={filesKnown} agents={agentsOf(p.crew)} />
        ))}
        <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} noun="crews" paged={paged.paged} />
      </SettingsCard>
      {showChanges && <ChangesCard crew={crew} crews={crews} entries={changes.entries} failed={changes.failed} />}
      <LevelsCard />
    </div>
  )
}
