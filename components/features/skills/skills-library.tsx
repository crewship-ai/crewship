"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { Bot, ChevronDown, ChevronRight, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { StatusPill } from "@/components/ui/status-pill"
import { cn } from "@/lib/utils"
import { HolderStack, SkillSourceMark } from "./skill-marks"
import {
  GROUP_LABEL,
  PAGE_SIZE,
  SORT_LABEL,
  domainMeta,
  groupSkills,
  skillName,
  skillSource,
  skillTrust,
  sortSkills,
  type SkillGroup,
  type SkillRow,
  type SkillSort,
} from "./skills-model"

// The library (Harbor 2 #3396): one table that works for 20 skills or 2 000.
// Sort and group live in the URL; 50 rows render and the next 50 load as you
// scroll (the API returns the whole catalog, so the window is client-side).
// On a phone the same rows are a list, not a squeezed table.

function fmt(n: number) {
  return n.toLocaleString("en-US").replace(/,/g, " ")
}

function Segmented<T extends string>({ label, value, options, onChange }: { label: string; value: T; options: [T, string][]; onChange: (v: T) => void }) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex rounded-md bg-foreground/[0.05] p-0.5">
      {options.map(([v, l]) => (
        <button
          key={v}
          type="button"
          role="radio"
          aria-checked={value === v}
          onClick={() => onChange(v)}
          className={cn(
            "kit-tap rounded px-2 py-0.5 text-label transition-colors",
            value === v ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
          )}
        >
          {l}
        </button>
      ))}
    </div>
  )
}

function UsedBy({ s }: { s: SkillRow }) {
  const holders = s.installed_on ?? []
  if (holders.length === 0) return <span className="text-muted-foreground-soft">Not on any agent</span>
  return <HolderStack agents={holders} max={4} />
}

interface RowProps {
  s: SkillRow
  checked: boolean
  selectable: boolean
  onCheck: () => void
  onOpen: () => void
}

function TableRow({ s, checked, selectable, onCheck, onOpen }: RowProps) {
  const trust = skillTrust(s)
  const src = skillSource(s)
  return (
    <tr
      data-slot="skill-row"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === "Enter") onOpen()
      }}
      className={cn("row-hover cursor-pointer border-b border-border last:border-0", checked && "bg-primary/[0.07]")}
    >
      <td className="w-9 py-2 pl-3.5 pr-0" onClick={(e) => e.stopPropagation()}>
        {selectable && <Checkbox checked={checked} onCheckedChange={onCheck} aria-label={`Select ${skillName(s)}`} />}
      </td>
      <td className="max-w-0 px-3.5 py-2">
        <span className="flex min-w-0 items-center gap-2.5">
          <SkillSourceMark skill={s} size={24} />
          <span className="min-w-0">
            <span className="block truncate font-medium text-foreground">{skillName(s)}</span>
            <span className="line-clamp-2 text-label text-muted-foreground">{s.description || "No description."}</span>
          </span>
        </span>
      </td>
      <td className="whitespace-nowrap px-3.5 py-2 text-label text-muted-foreground">{src.label}</td>
      <td className="whitespace-nowrap px-3.5 py-2 text-muted-foreground">{domainMeta(s.category).label}</td>
      <td className="whitespace-nowrap px-3.5 py-2">
        <StatusPill tone={trust.tone} label={trust.label} />
      </td>
      <td className="whitespace-nowrap px-3.5 py-2 text-label">
        <UsedBy s={s} />
      </td>
      <td className="whitespace-nowrap px-3.5 py-2 text-right font-mono tabular-nums">{s.usage?.uses_7d || "—"}</td>
    </tr>
  )
}

function Table({ rows, sel, selectable, onCheck, onOpen }: { rows: SkillRow[]; sel: Set<string>; selectable: boolean; onCheck: (id: string) => void; onOpen: (id: string) => void }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full table-fixed border-collapse text-control" data-slot="skills-table">
        <colgroup>
          <col className="w-9" />
          <col />
          <col className="w-44" />
          <col className="w-28" />
          <col className="w-32" />
          <col className="w-36" />
          <col className="w-24" />
        </colgroup>
        <thead>
          <tr className="border-b border-border bg-muted/50 text-left text-label text-muted-foreground">
            <th className="py-2 pl-3.5 pr-0 font-medium">
              <span className="sr-only">Select</span>
            </th>
            <th className="px-3.5 py-2 font-medium">Skill</th>
            <th className="px-3.5 py-2 font-medium">Source</th>
            <th className="px-3.5 py-2 font-medium">Domain</th>
            <th className="px-3.5 py-2 font-medium">Trust</th>
            <th className="px-3.5 py-2 font-medium">Used by</th>
            <th className="px-3.5 py-2 text-right font-medium">Uses · 7 d</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((s) => (
            <TableRow key={s.id} s={s} checked={sel.has(s.id)} selectable={selectable} onCheck={() => onCheck(s.id)} onOpen={() => onOpen(s.id)} />
          ))}
        </tbody>
      </table>
    </div>
  )
}

/** Phone: brand mark, name, trust and who holds it — no table to squeeze. */
function PhoneRows({ rows, onOpen }: { rows: SkillRow[]; onOpen: (id: string) => void }) {
  return (
    <ul data-slot="skills-phone-rows">
      {rows.map((s) => {
        const trust = skillTrust(s)
        return (
          <li key={s.id} className="border-b border-border last:border-0">
            <button type="button" data-slot="skill-row" onClick={() => onOpen(s.id)} className="row-hover flex min-h-12 coarse:min-h-[44px] w-full items-center gap-3 px-3.5 py-2.5 text-left">
              <SkillSourceMark skill={s} size={28} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-control font-medium">{skillName(s)}</span>
                <span className="block truncate text-label text-muted-foreground">
                  {(s.installed_on?.length ?? 0) > 0 ? `${s.installed_on!.length} ${s.installed_on!.length === 1 ? "agent" : "agents"}` : "Not on any agent"} · {skillSource(s).label}
                </span>
              </span>
              <StatusPill tone={trust.tone} label={trust.label} />
              <ChevronRight className="size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
            </button>
          </li>
        )
      })}
    </ul>
  )
}

export interface SkillsLibraryProps {
  rows: SkillRow[]
  sort: SkillSort
  group: SkillGroup
  onSort: (s: SkillSort) => void
  onGroup: (g: SkillGroup) => void
  onOpen: (id: string) => void
  /** Show all of a group: sets that group's facet. */
  onShowGroup: (group: Exclude<SkillGroup, "none">, key: string) => void
  /** Managers and above may give skills; others see no checkboxes. */
  canGive: boolean
  onGiveToCrew: (rows: SkillRow[]) => void
  phone: boolean
  /** Rendered in place of the table when nothing matches. */
  empty?: React.ReactNode
}

export function SkillsLibrary({ rows, sort, group, onSort, onGroup, onOpen, onShowGroup, canGive, onGiveToCrew, phone, empty }: SkillsLibraryProps) {
  const sorted = useMemo(() => sortSkills(rows, sort), [rows, sort])
  const [limit, setLimit] = useState(PAGE_SIZE)
  const [sel, setSel] = useState<Set<string>>(new Set())
  const sentinel = useRef<HTMLDivElement>(null)

  // A new list starts at the first 50 again.
  useEffect(() => setLimit(PAGE_SIZE), [rows, sort, group])
  // Drop selections that the filters hid.
  useEffect(() => {
    setSel((cur) => {
      const ids = new Set(rows.map((r) => r.id))
      const next = new Set([...cur].filter((id) => ids.has(id)))
      return next.size === cur.size ? cur : next
    })
  }, [rows])

  const more = group === "none" && sorted.length > limit
  useEffect(() => {
    const el = sentinel.current
    if (!el || !more || typeof IntersectionObserver === "undefined") return
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) setLimit((l) => l + PAGE_SIZE)
    })
    io.observe(el)
    return () => io.disconnect()
  }, [more, limit])

  const check = (id: string) =>
    setSel((s) => {
      const next = new Set(s)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  const selectable = canGive && !phone

  const toolbar = (
    <div className="flex flex-wrap items-center gap-2" data-slot="skills-library-toolbar">
      <span className="text-label tabular-nums text-muted-foreground">{fmt(sorted.length)} skills</span>
      <span className="flex-1" />
      <span className="text-label text-muted-foreground-soft">Sort</span>
      <Segmented label="Sort" value={sort} onChange={onSort} options={(Object.keys(SORT_LABEL) as SkillSort[]).map((k) => [k, SORT_LABEL[k]])} />
      {!phone && (
        <>
          <span className="text-label text-muted-foreground-soft">Group</span>
          <Segmented label="Group" value={group} onChange={onGroup} options={(Object.keys(GROUP_LABEL) as SkillGroup[]).map((k) => [k, GROUP_LABEL[k]])} />
        </>
      )}
    </div>
  )

  const list = (r: SkillRow[]) =>
    phone ? <PhoneRows rows={r} onOpen={onOpen} /> : <Table rows={r} sel={sel} selectable={selectable} onCheck={check} onOpen={onOpen} />

  let body: React.ReactNode
  if (sorted.length === 0) body = empty
  else if (group !== "none" && !phone) {
    body = groupSkills(sorted, group).map((g) => (
      <section key={g.key} className="overflow-hidden rounded-card border border-border bg-card" aria-label={`${g.label} · ${g.rows.length}`}>
        <header className="flex items-center gap-2.5 px-4 py-3">
          {group === "source" ? <SkillSourceMark skill={g.rows[0]} size={24} /> : null}
          <h3 className="text-control font-semibold">
            {g.label} <span className="font-normal text-muted-foreground">· {g.rows.length}</span>
          </h3>
        </header>
        {list(g.rows.slice(0, 5))}
        {g.rows.length > 5 && (
          <button type="button" onClick={() => onShowGroup(group, g.key)} className="kit-tap w-full border-t border-border px-4 py-2 text-left text-label text-primary-hover hover:bg-foreground/[0.03]">
            Show all {g.rows.length} in {g.label} ›
          </button>
        )}
      </section>
    ))
  } else {
    body = (
      <>
        <div className="overflow-hidden rounded-card border border-border bg-card">{list(sorted.slice(0, limit))}</div>
        <div className="flex flex-wrap items-center gap-3" ref={sentinel}>
          {more && (
            <Button variant="outline" size="sm" onClick={() => setLimit((l) => l + PAGE_SIZE)}>
              <ChevronDown className="size-3.5" aria-hidden />
              Load {PAGE_SIZE} more
            </Button>
          )}
          {sorted.length > PAGE_SIZE && (
            <span className="text-label text-muted-foreground-soft">
              Showing {fmt(Math.min(limit, sorted.length))} of {fmt(sorted.length)}
            </span>
          )}
        </div>
      </>
    )
  }

  const selectedRows = sorted.filter((s) => sel.has(s.id))

  return (
    <div className="flex flex-col gap-3" data-slot="skills-library">
      {toolbar}
      {body}
      {selectedRows.length > 0 && (
        <div
          role="toolbar"
          aria-label="Selected skills"
          data-slot="skills-bulk-bar"
          className="sticky bottom-4 z-10 mx-auto flex items-center gap-1.5 rounded-lg bg-foreground py-1.5 pl-3.5 pr-2 text-control text-background shadow-xl"
        >
          <span className="tabular-nums">{selectedRows.length} selected</span>
          <span className="mx-1 h-4 w-px bg-background/25" aria-hidden />
          <Button size="sm" variant="ghost" className="text-background hover:bg-background/15 hover:text-background" onClick={() => onGiveToCrew(selectedRows)}>
            <Bot className="size-3.5" aria-hidden />
            Give to crew
          </Button>
          <Button size="icon-sm" variant="ghost" aria-label="Clear selection" className="text-background hover:bg-background/15 hover:text-background" onClick={() => setSel(new Set())}>
            <X className="size-3.5" />
          </Button>
        </div>
      )}
    </div>
  )
}
