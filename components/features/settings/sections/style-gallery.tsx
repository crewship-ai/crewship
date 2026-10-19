"use client"

import { useState } from "react"

import { cn } from "@/lib/utils"
import { ACCENTS } from "@/lib/theme/accents"
import { STYLES, STYLE_CATEGORIES, renderedStyle, type Scheme, type StyleInfo, type StyleState } from "@/lib/theme/appearance-style"
import { stylePreviewAttrs } from "@/lib/theme/style-preview"

// The Style card's gallery (owner, 19 Oct: 42 big previews made it five
// screens long). One live preview shows the chosen style, or the one under
// the pointer, with its name and blurb; below it category chips and a compact
// grid of palettes. The preview is a small page drawn with the app's own
// tokens inside an element that wears the style (stylePreviewAttrs), so it
// shows the real palette, corners and accent of that style in the scheme the
// style will have, not a picture that can drift from the stylesheet. The
// palettes are the registry's swatch: rail, card, accent, dark ground.

function accentSwatch(style: StyleInfo): string {
  return ACCENTS.find((a) => a.id === style.accent)?.swatch ?? style.swatch[2]
}

function StylePreview({ style, scheme }: { style: StyleInfo; scheme: Scheme }) {
  const { className, ...attrs } = stylePreviewAttrs(style.id, style.schemes.length === 1 ? style.schemes[0] : scheme)
  return (
    <div
      aria-hidden
      data-testid="style-preview"
      {...attrs}
      className={cn(className, "flex h-28 w-full overflow-hidden rounded-lg border border-border bg-background text-foreground sm:w-64 sm:shrink-0")}
    >
      <div className="flex w-1/4 flex-col gap-1.5 border-r border-sidebar-border bg-sidebar p-2">
        <span className="h-2 w-3/4 rounded-full" style={{ background: accentSwatch(style) }} />
        <span className="h-1.5 w-full rounded-full bg-icon-faint" />
        <span className="h-1.5 w-2/3 rounded-full bg-icon-faint" />
        <span className="h-1.5 w-3/4 rounded-full bg-icon-faint" />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-2 p-2.5">
        <span className="h-2 w-1/2 rounded-full bg-foreground" />
        <span className="h-1.5 w-3/4 rounded-full bg-muted-foreground" />
        <div className="flex flex-1 items-end justify-between gap-1 rounded-card border border-border bg-card p-2">
          <span className="h-1.5 w-1/3 rounded-full bg-muted-foreground-soft" />
          <span className="h-3 w-8 rounded-md" style={{ background: accentSwatch(style) }} />
        </div>
      </div>
    </div>
  )
}

function SchemeChip({ style }: { style: StyleInfo }) {
  if (style.schemes.length !== 1) return null
  return <span className="rounded-full bg-chip-neutral-bg px-1.5 text-micro text-chip-neutral-fg">{style.schemes[0] === "dark" ? "Dark only" : "Light only"}</span>
}

const ALL = "All"

export function StyleGallery({ state, scheme, onPick }: { state: StyleState | null; scheme: Scheme; onPick: (id: string) => void }) {
  const active = renderedStyle(state, scheme)
  const [category, setCategory] = useState<string>(ALL)
  const [hovered, setHovered] = useState<string | null>(null)
  const shown = STYLES.find((s) => s.id === (hovered ?? active)) ?? STYLES[0]
  const used = STYLE_CATEGORIES.filter((c) => STYLES.some((s) => s.category === c))
  const tiles = category === ALL ? STYLES : STYLES.filter((s) => s.category === category)
  return (
    <div className="space-y-4 p-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:gap-4">
        <StylePreview style={shown} scheme={scheme} />
        <div data-testid="style-current" className="min-w-0 space-y-1" aria-live="polite">
          <p className="flex flex-wrap items-center gap-2">
            <span className="text-body font-medium text-foreground">{shown.name}</span>
            <SchemeChip style={shown} />
            {shown.id !== active && <span className="text-label text-muted-foreground">Preview</span>}
          </p>
          <p className="text-label leading-snug text-muted-foreground">{shown.blurb}</p>
          <p className="text-label text-muted-foreground-soft">{shown.category}</p>
        </div>
      </div>

      <div role="group" aria-label="Style categories" className="flex flex-wrap gap-1.5">
        {[ALL, ...used].map((c) => (
          <button
            key={c}
            type="button"
            aria-pressed={category === c}
            onClick={() => setCategory(c)}
            className={cn(
              "h-7 rounded-full border px-2.5 text-label transition-colors coarse:min-h-[44px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              category === c ? "border-primary bg-primary/10 text-foreground" : "border-border text-muted-foreground hover:text-foreground",
            )}
          >
            {c}
          </button>
        ))}
      </div>

      <div role="radiogroup" aria-label="Style" data-testid="style-grid" className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-6">
        {tiles.map((style) => {
          const checked = style.id === active
          return (
            <button
              key={style.id}
              type="button"
              role="radio"
              aria-checked={checked}
              aria-label={style.name}
              title={style.blurb}
              onClick={() => onPick(style.id)}
              onMouseEnter={() => setHovered(style.id)}
              onMouseLeave={() => setHovered(null)}
              onFocus={() => setHovered(style.id)}
              onBlur={() => setHovered(null)}
              className={cn(
                "flex min-w-0 flex-col gap-1.5 rounded-lg border p-1.5 text-left outline-none transition-colors coarse:min-h-[44px]",
                "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
                checked ? "border-primary ring-1 ring-primary" : "border-border",
              )}
            >
              <span className="flex h-6 overflow-hidden rounded-md border border-border" aria-hidden>
                {style.swatch.map((color, i) => (
                  <span key={i} data-testid="style-swatch" className="flex-1" style={{ background: color }} />
                ))}
              </span>
              <span className="truncate px-0.5 text-label text-foreground">{style.name}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}
