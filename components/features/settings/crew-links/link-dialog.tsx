"use client"

import * as React from "react"
import { ArrowLeft, ArrowRight, Link2 } from "lucide-react"

import { Combobox, type ComboboxOption } from "@/components/ui/combobox"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"

import { connectionBetween, levelSentence, pairLevels, type Crew, type Level } from "./crew-links-model"
import { CrewTile } from "./crew-profile"
import { LevelLadder } from "./level-ladder"
import type { useCrewLinks } from "./use-crew-links"

type Links = ReturnType<typeof useCrewLinks>

export interface LinkDialogInit {
  /** "edit" when the pair is already linked, "new" otherwise. */
  mode: "new" | "edit"
  a?: Crew
  b?: Crew
}

/**
 * The link dialog ("New link" and edit): two crew pickers, each leaving out
 * the other, and both ladders. One Save writes everything; nothing is written
 * before it, and closing with changes asks first (CreateSurface `dirty`).
 */
export function LinkDialog({ init, crews, dups, links, filesKnown, onClose }: {
  init: LinkDialogInit | null
  crews: Crew[]
  dups: Set<string>
  links: Links
  filesKnown: boolean
  onClose: () => void
}) {
  const [aId, setAId] = React.useState<string | null>(null)
  const [bId, setBId] = React.useState<string | null>(null)
  const [out, setOut] = React.useState<Level>("off")
  const [inn, setInn] = React.useState<Level>("off")
  const a = crews.find((c) => c.id === aId)
  const b = crews.find((c) => c.id === bId)
  const open = init !== null

  React.useEffect(() => {
    if (!init) return
    setAId(init.a?.id ?? null)
    setBId(init.b?.id ?? null)
  }, [init])

  // What the server holds for the chosen pair; the ladders start there each time the pair changes.
  const held = React.useMemo(() => (a && b ? pairLevels(connectionBetween(links.connections, a.id, b.id), a.id) : null), [a, b, links.connections])
  const pairKey = a && b ? `${a.id}|${b.id}` : ""
  React.useEffect(() => {
    setOut(held?.out ?? "off")
    setInn(held?.in ?? "off")
    // The ladders reset when the pair changes, not when the server list reloads under them.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pairKey, open])

  const dirty = Boolean(held) && (out !== held!.out || inn !== held!.in)
  const label = (c: Crew) => (dups.has(c.name) ? `${c.name} (${c.slug})` : c.name)
  const options = (skip: string | null): ComboboxOption[] =>
    crews.filter((c) => c.id !== skip).map((c) => ({ value: c.id, label: label(c), description: dups.has(c.name) ? undefined : c.slug, keywords: [c.slug], icon: <CrewTile crew={c} /> }))

  const save = async () => {
    if (!a || !b || !dirty || links.pending) return
    const result = await links.applyLevels(a, b, { out, in: inn })
    if (result !== "failed") onClose()
  }

  const title = init?.mode === "edit" ? "Edit link" : "New link"
  return (
    <CreateSurface open={open} onOpenChange={(o) => { if (!o) onClose() }} size="md" dirty={dirty} ariaLabel={title} onSubmit={() => void save()}>
      {open && (
        <>
          <CreateSurfaceHeader icon={Link2} title={title} description="Two crews, and what each may hand to the other." onClose={onClose} />
          <CreateSurfaceBody className="flex flex-col gap-3" data-slot="crew-link-dialog">
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="flex min-w-0 flex-col gap-1.5 text-control">
                <span className="font-medium">Crew</span>
                <Combobox modal value={aId} onChange={(v) => setAId(v)} options={options(bId)} ariaLabel="Crew" placeholder="Choose a crew…" searchPlaceholder="Search crews…" />
              </label>
              <label className="flex min-w-0 flex-col gap-1.5 text-control">
                <span className="font-medium">Works with</span>
                <Combobox modal value={bId} onChange={(v) => setBId(v)} options={options(aId)} ariaLabel="Works with" placeholder="Choose a crew…" searchPlaceholder="Search crews…" />
              </label>
            </div>
            {a && b ? (
              <div className="overflow-hidden rounded-card border border-border">
                <DirectionRow from={a} to={b} level={out} onChange={setOut} filesKnown={filesKnown} icon={ArrowRight} />
                <DirectionRow from={b} to={a} level={inn} onChange={setInn} filesKnown={filesKnown} icon={ArrowLeft} />
              </div>
            ) : (
              <p className="rounded-lg border border-dashed border-border px-3 py-4 text-label text-muted-foreground">Choose two crews to set what each may hand to the other.</p>
            )}
            <p className="text-label text-muted-foreground">
              Search finds any of the {crews.length} crews. Each level includes the ones before it. If someone else changed this link meanwhile, saving is refused and the link reloads.
            </p>
          </CreateSurfaceBody>
          <CreateSurfaceFooter onCancel={onClose} primaryLabel="Save link" onPrimary={() => void save()} primaryDisabled={!a || !b || !dirty} busy={links.pending} />
        </>
      )}
    </CreateSurface>
  )
}

function DirectionRow({ from, to, level, onChange, filesKnown, icon: Icon }: {
  from: Crew; to: Crew; level: Level; onChange: (l: Level) => void; filesKnown: boolean; icon: typeof ArrowRight
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border px-3 py-2.5 last:border-b-0">
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <div className="min-w-0 flex-[1_1_12rem]">
        <div className="text-control font-medium">{from.name} hands work to {to.name}</div>
        <div className="text-label text-muted-foreground">{levelSentence(from.name, to.name, level)}</div>
      </div>
      <LevelLadder ariaLabel={`${from.name} to ${to.name}`} value={level} onChange={onChange} filesKnown={filesKnown} />
    </div>
  )
}
