"use client"

import { cn } from "@/lib/utils"
import { ACCENTS } from "@/lib/theme/accents"
import { STYLES, STYLE_CATEGORIES, renderedStyle, type Scheme, type StyleInfo, type StyleState } from "@/lib/theme/appearance-style"
import { stylePreviewAttrs } from "@/lib/theme/style-preview"

// The Style card's gallery: every style in the registry, by category, each
// with a live preview. The preview is a small page drawn with the app's own
// tokens inside an element that wears the style (stylePreviewAttrs), so it
// shows the real palette, corners and accent of that style in the scheme the
// style will have — not a picture that can drift from the stylesheet.

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
      className={cn(className, "flex h-20 w-full overflow-hidden rounded-lg border border-border bg-background text-foreground")}
    >
      <div className="flex w-1/4 flex-col gap-1 border-r border-sidebar-border bg-sidebar p-1.5">
        <span className="h-1.5 w-3/4 rounded-full" style={{ background: accentSwatch(style) }} />
        <span className="h-1 w-full rounded-full bg-icon-faint" />
        <span className="h-1 w-2/3 rounded-full bg-icon-faint" />
      </div>
      <div className="flex min-w-0 flex-1 flex-col gap-1.5 p-2">
        <span className="h-1.5 w-1/2 rounded-full bg-foreground" />
        <span className="h-1 w-3/4 rounded-full bg-muted-foreground" />
        <div className="flex flex-1 items-end justify-between gap-1 rounded-card border border-border bg-card p-1.5">
          <span className="h-1 w-1/3 rounded-full bg-muted-foreground-soft" />
          <span className="h-2.5 w-6 rounded-md" style={{ background: accentSwatch(style) }} />
        </div>
      </div>
    </div>
  )
}

export function StyleGallery({ state, scheme, onPick }: { state: StyleState | null; scheme: Scheme; onPick: (id: string) => void }) {
  const active = renderedStyle(state, scheme)
  return (
    <div role="radiogroup" aria-label="Style" className="space-y-5 p-4">
      {STYLE_CATEGORIES.map((category) => {
        const inCategory = STYLES.filter((s) => s.category === category)
        if (inCategory.length === 0) return null
        return (
          <div key={category} role="group" aria-label={category} className="space-y-2">
            <h4 className="text-label font-medium uppercase tracking-wide text-muted-foreground">{category}</h4>
            <div data-testid="style-grid" className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {inCategory.map((style) => {
                const checked = style.id === active
                return (
                  <button
                    key={style.id}
                    type="button"
                    role="radio"
                    aria-checked={checked}
                    aria-label={style.name}
                    onClick={() => onPick(style.id)}
                    className={cn(
                      "flex min-w-0 flex-col gap-2 rounded-xl border p-1.5 text-left outline-none transition-colors coarse:min-h-[44px]",
                      "hover:border-line-strong focus-visible:ring-2 focus-visible:ring-ring/50",
                      checked ? "border-primary ring-1 ring-primary" : "border-border",
                    )}
                  >
                    <StylePreview style={style} scheme={scheme} />
                    <span className="flex min-w-0 flex-col gap-0.5 px-1 pb-0.5">
                      <span className="flex items-center gap-2">
                        <span className={cn("text-control", checked ? "font-medium text-foreground" : "text-foreground")}>{style.name}</span>
                        {style.schemes.length === 1 && (
                          <span className="rounded-full bg-chip-neutral-bg px-1.5 text-micro text-chip-neutral-fg">{style.schemes[0] === "dark" ? "Dark only" : "Light only"}</span>
                        )}
                      </span>
                      <span className="text-label leading-snug text-muted-foreground">{style.blurb}</span>
                    </span>
                  </button>
                )
              })}
            </div>
          </div>
        )
      })}
    </div>
  )
}
