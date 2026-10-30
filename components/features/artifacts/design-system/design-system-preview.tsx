"use client"
import { useMemo, type ReactNode } from "react"
import { parseDesignSystem, type DesignSystem } from "./manifest"

const SAMPLE = "Ship the crew, keep the log."

function Section({ title, count, children }: { title: string; count: number; children: ReactNode }) {
  return <section aria-label={title} className="space-y-2">
    <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{title} <span className="font-normal">({count})</span></h3>
    {count === 0 ? <p className="text-sm text-muted-foreground">None defined.</p> : children}
  </section>
}

/** Read-only token sheet. Values were admitted as literals by parseDesignSystem; nothing is injected as CSS text. */
export function DesignSystemSheet({ system }: { system: DesignSystem }) {
  return <div className="space-y-6 p-4 text-foreground">
    {system.name && <h2 className="text-lg font-semibold">{system.name}</h2>}
    <Section title="Colors" count={system.colors.length}>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(120px,1fr))] gap-3">
        {system.colors.map(color => <li key={color.name} data-token={`color.${color.name}`} className="overflow-hidden rounded-md border">
          <div aria-hidden className="h-14" style={{ backgroundColor: color.value }} />
          <div className="px-2 py-1.5 text-xs"><b className="block truncate font-medium">{color.name}</b><code className="text-muted-foreground">{color.value}</code></div>
        </li>)}
      </ul>
    </Section>
    <Section title="Typography" count={system.typography.length}>
      <ul className="divide-y rounded-md border">
        {system.typography.map(style => <li key={style.name} data-token={`typography.${style.name}`} className="grid gap-1 px-3 py-2 sm:grid-cols-[160px_minmax(0,1fr)] sm:items-baseline">
          <span className="text-xs text-muted-foreground"><b className="font-medium text-foreground">{style.name}</b> · {style.size}px{style.weight ? ` / ${style.weight}` : ""}{style.lineHeight ? ` / ${style.lineHeight}` : ""}{style.fontFamily ? ` · ${style.fontFamily}` : ""}</span>
          <span className="truncate" style={{ fontSize: style.size, fontWeight: style.weight ?? undefined, lineHeight: style.lineHeight ?? undefined, fontFamily: style.fontFamily ?? undefined }}>{SAMPLE}</span>
        </li>)}
      </ul>
    </Section>
    <Section title="Spacing" count={system.spacing.length}>
      <ul className="space-y-1.5">
        {system.spacing.map(space => <li key={space.name} data-token={`spacing.${space.name}`} className="flex items-center gap-3 text-xs">
          <span className="w-24 shrink-0 truncate font-medium">{space.name}</span>
          <code className="w-14 shrink-0 text-muted-foreground">{space.value}px</code>
          <span aria-hidden className="h-3 max-w-full rounded-sm bg-primary/60" style={{ width: space.value }} />
        </li>)}
      </ul>
    </Section>
    <Section title="Radii" count={system.radii.length}>
      <ul className="flex flex-wrap gap-4">
        {system.radii.map(radius => <li key={radius.name} data-token={`radius.${radius.name}`} className="flex flex-col items-center gap-1 text-xs">
          <span aria-hidden className="size-14 border-2 border-primary/60 bg-muted" style={{ borderRadius: radius.value }} />
          <b className="font-medium">{radius.name}</b><code className="text-muted-foreground">{radius.value}px</code>
        </li>)}
      </ul>
    </Section>
    {system.components.length > 0 && <Section title="Components" count={system.components.length}>
      <ul className="divide-y rounded-md border text-sm">
        {system.components.map(component => <li key={component.name} className="px-3 py-2"><b className="font-medium">{component.name}</b>{component.description && <p className="text-xs text-muted-foreground">{component.description}</p>}</li>)}
      </ul>
    </Section>}
  </div>
}

/** Preview for one retained artifact version; the caller passes that version's manifest files. */
export function ArtifactDesignSystemPreview({ files, title }: { files: readonly { path: string; content: string }[]; title: string }) {
  const system = useMemo(() => parseDesignSystem(files), [files])
  return <div aria-label={title} className="h-full overflow-auto bg-background">
    {system ? <DesignSystemSheet system={system} /> : <p role="alert" className="p-4 text-sm">This Design System file is invalid or uses an unsupported format.</p>}
  </div>
}
