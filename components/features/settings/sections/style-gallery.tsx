"use client"

import { useState, type ReactNode } from "react"

import { cn } from "@/lib/utils"
import { ACCENTS } from "@/lib/theme/accents"
import { STYLES, STYLE_LOOKS, renderedStyle, type Scheme, type StyleInfo, type StyleState } from "@/lib/theme/appearance-style"
import { stylePreviewAttrs } from "@/lib/theme/style-preview"

// The Style row's gallery (Settings › Appearance › Colours › Change style).
// Opened inline under the row: chips for the looks (neutral, warm, violet,
// made for dark, with counts) and a compact grid of palettes that follows the
// card's width (container queries: a tablet's Settings column is narrow at a
// desk-sized window). The live preview of the chosen style is the "Your look"
// line above; StylePreview draws a small page with the app's own tokens inside
// an element that wears the style (stylePreviewAttrs), so it shows the real
// palette, corners and accent in the scheme the style will have, not a picture
// that can drift from the stylesheet. The palettes are the registry's swatch:
// rail, card, accent, dark ground. The corners, cards and lines rows follow
// as children.

function accentSwatch(style: StyleInfo): string {
  // A pack's primary action is its own colour (not the Crewship accent it suggests).
  if (style.pack) return style.swatch[2]
  return ACCENTS.find((a) => a.id === style.accent)?.swatch ?? style.swatch[2]
}

/** `accentHex` paints the person's own accent instead of the style's suggestion. */
export function StylePreview({ style, scheme, accentHex }: { style: StyleInfo; scheme: Scheme; accentHex?: string }) {
  const accentColor = accentHex ?? accentSwatch(style)
  const { className, ...attrs } = stylePreviewAttrs(style.id, style.schemes.length === 1 ? style.schemes[0] : scheme)
  return (
    <div
      aria-hidden
      data-testid="style-preview"
      {...attrs}
      className={cn(className, "flex h-28 w-full overflow-hidden rounded-lg border border-border bg-background text-foreground sm:w-64 sm:shrink-0")}
    >
      <div className="flex w-1/4 flex-col gap-1.5 border-r border-sidebar-border bg-sidebar p-2">
        <span className="h-2 w-3/4 rounded-full" style={{ background: accentColor }} />
        <span className="h-1.5 w-full rounded-full bg-icon-faint" />
        <span className="h-1.5 w-2/3 rounded-full bg-icon-faint" />
        <span className="h-1.5 w-3/4 rounded-full bg-icon-faint" />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-2 p-2.5">
        <span className="h-2 w-1/2 rounded-full bg-foreground" />
        <span className="h-1.5 w-3/4 rounded-full bg-muted-foreground" />
        <div className="flex flex-1 items-end justify-between gap-1 rounded-card border border-border bg-card p-2">
          {style.pack ? (
            <span data-testid="pack-sunset" className="h-2 w-1/3 rounded-full" style={{ background: "var(--pack-sunset)" }} />
          ) : (
            <span className="h-1.5 w-1/3 rounded-full bg-muted-foreground-soft" />
          )}
          <span className="h-3 w-8 rounded-md" style={{ background: accentColor }} />
        </div>
      </div>
    </div>
  )
}

const ALL = "all"

export function StyleGallery({ state, scheme, onPick, children }: { state: StyleState | null; scheme: Scheme; onPick: (id: string) => void; children?: ReactNode }) {
  const active = renderedStyle(state, scheme)
  const [look, setLook] = useState<string>(ALL)
  const tiles = look === ALL ? STYLE_LOOKS.flatMap((l) => l.styles) : (STYLE_LOOKS.find((l) => l.id === look)?.styles ?? [])
  const shown = tiles.map((id) => STYLES.find((s) => s.id === id)).filter((s): s is StyleInfo => !!s)
  const chips = [{ id: ALL, label: "All", count: undefined as number | undefined }, ...STYLE_LOOKS.map((l) => ({ id: l.id as string, label: l.label, count: l.styles.length }))]
  return (
    <div data-testid="style-gallery" className="border-t border-border bg-surface-subtle">
      <div className="space-y-3 p-4">
        <div role="group" aria-label="Looks" className="flex flex-wrap gap-1.5">
          {chips.map((c) => (
            <button
              key={c.id}
              type="button"
              aria-pressed={look === c.id}
              onClick={() => setLook(c.id)}
              className={cn(
                "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-label transition-colors coarse:min-h-[44px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                look === c.id ? "border-primary bg-primary/10 text-foreground" : "border-border bg-card text-muted-foreground hover:text-foreground",
              )}
            >
              {c.label}
              {c.count !== undefined && <span className="tabular-nums text-muted-foreground-soft">{c.count}</span>}
            </button>
          ))}
        </div>

        <div className="@container">
          <div role="radiogroup" aria-label="Style" data-testid="style-grid" className="grid grid-cols-2 gap-2 @lg:grid-cols-4 @2xl:grid-cols-6">
            {shown.map((style) => {
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
                  className={cn(
                    "flex min-w-0 flex-col gap-1.5 rounded-lg border bg-card p-1.5 text-left outline-none transition-colors coarse:min-h-[44px]",
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
      </div>
      {children}
    </div>
  )
}
