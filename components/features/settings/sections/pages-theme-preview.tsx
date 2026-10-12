"use client"

import { useEffect } from "react"
import { X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { pageThemeVars, type PageTheme } from "@/lib/pages/theme"

const LABELS: Record<keyof PageTheme, string> = {
  accent: "Brand accent",
  background: "Background",
  surface: "Cards",
  text: "Text",
  muted: "Secondary text",
  border: "Borders",
}

/**
 * Settings › General › Pages appearance › Preview.
 *
 * A non-modal panel over the right half of the screen: the settings stay
 * usable beside it, and the palette strip at the top edits the same draft the
 * card does. Nothing here saves — Save stays on the card.
 */
export function PagesThemePreviewPanel({
  theme,
  dirty,
  editable,
  onChange,
  onClose,
}: {
  theme: PageTheme
  dirty: boolean
  editable: boolean
  onChange: (key: keyof PageTheme, value: string) => void
  onClose: () => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose() }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [onClose])

  return (
    <aside
      role="dialog"
      aria-modal="false"
      aria-label="Page preview"
      className="fixed inset-y-0 right-0 z-40 flex w-full flex-col border-l border-border bg-background shadow-[0_0_80px_-30px_var(--shadow-color)] sm:w-1/2"
    >
      <div className="flex items-center gap-3 border-b border-border bg-card px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="text-sm font-semibold tracking-[-0.01em]">Page preview</div>
          <div className="text-label text-muted-foreground">
            {dirty ? "Showing unsaved colours — Save on the card applies them." : "How a Page application looks with this palette."}
          </div>
        </div>
        <Button variant="outline" size="sm" className="h-8 gap-1.5 text-xs" onClick={onClose} aria-label="Close preview">
          <X className="h-3.5 w-3.5" /> Close preview
        </Button>
      </div>

      {editable && (
        <div className="flex flex-wrap items-center gap-3 border-b border-border bg-surface-subtle px-4 py-2.5" aria-label="Palette">
          {(Object.keys(LABELS) as (keyof PageTheme)[]).map((key) => (
            <span key={key} className="flex items-center gap-1.5 text-label text-muted-foreground" title={LABELS[key]}>
              <input
                type="color"
                aria-label={`${LABELS[key]} colour`}
                value={theme[key]}
                onChange={(e) => onChange(key, e.target.value)}
                className="h-6 w-7 cursor-pointer rounded border border-border bg-transparent"
              />
              <span aria-hidden>{LABELS[key]}</span>
            </span>
          ))}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto">
        <SamplePage theme={theme} />
      </div>
    </aside>
  )
}

/**
 * A small Page application drawn the way the Pages starter draws one
 * (tools/pages-build/starter/src/style.css), from the same
 * --crewship-page-* variables the SDK sets (pageThemeVars).
 */
export function SamplePage({ theme }: { theme: PageTheme }) {
  const { colorScheme, ...vars } = pageThemeVars(theme)
  const v = (name: keyof PageTheme | "on-accent") => `var(--crewship-page-${name})`
  const card = { background: v("surface"), border: `1px solid ${v("border")}`, borderRadius: 16, padding: 20, minWidth: 0 } as const
  const pill = (bg: string, fg: string) => ({ fontSize: 11, borderRadius: 20, padding: "4px 9px", background: bg, color: fg }) as const
  return (
    <div
      data-testid="pages-theme-preview"
      style={{ ...vars, colorScheme, background: v("background"), color: v("text"), fontFamily: "system-ui, sans-serif", minHeight: "100%" }}
    >
      <main style={{ maxWidth: 1200, margin: "auto", padding: "36px 28px" }}>
        <header style={{ marginBottom: 28 }}>
          <div style={{ fontSize: 11, letterSpacing: ".18em", color: v("accent") }}>OPERATIONS</div>
          <h1 style={{ fontSize: 32, letterSpacing: "-.04em", margin: "12px 0", fontWeight: 650 }}>Infrastructure overview</h1>
          <p style={{ color: v("muted"), lineHeight: 1.6, margin: 0, maxWidth: "60ch" }}>
            Servers, deployments and backups in one place, refreshed by your crew every five minutes.
          </p>
          <div style={{ display: "flex", gap: 10, marginTop: 18 }}>
            <span style={{ background: v("accent"), color: v("on-accent"), borderRadius: 10, padding: "8px 14px", fontSize: 13, fontWeight: 600 }}>Refresh data</span>
            <span style={{ border: `1px solid ${v("border")}`, borderRadius: 10, padding: "8px 14px", fontSize: 13 }}>Export</span>
          </div>
        </header>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))", gap: 16 }}>
          {[
            { title: "Servers", value: "12 / 12", note: "All healthy", state: pill("#16443a", "#89ead1"), word: "fresh" },
            { title: "Deployments", value: "3 today", note: "Last 14 min ago", state: pill("#514022", "#ffd58e"), word: "stale" },
            { title: "Backups", value: "1 failed", note: "db-main at 02:00", state: pill("#542d36", "#ffb3be"), word: "failed" },
          ].map((c) => (
            <article key={c.title} style={card}>
              <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
                <h2 style={{ fontSize: 15, fontWeight: 600, margin: 0 }}>{c.title}</h2>
                <span style={c.state}>{c.word}</span>
              </div>
              <div style={{ fontSize: 26, fontWeight: 650, letterSpacing: "-.03em", marginTop: 12 }}>{c.value}</div>
              <p style={{ color: v("muted"), margin: "4px 0 0", fontSize: 13 }}>{c.note}</p>
            </article>
          ))}
        </div>
        <article style={{ ...card, marginTop: 16 }}>
          <h2 style={{ fontSize: 15, fontWeight: 600, margin: "0 0 10px" }}>Latest note from the crew</h2>
          <pre style={{ whiteSpace: "pre-wrap", fontSize: 13, lineHeight: 1.6, margin: 0, color: v("text") }}>
            {"Backup of db-main failed at 02:00 (disk 94% full).\nRetry scheduled for 02:30; the crew will clean old snapshots first."}
          </pre>
        </article>
        <footer style={{ borderTop: `1px solid ${v("border")}`, paddingTop: 14, marginTop: 24, fontSize: 11, color: v("muted") }}>
          Updated 2 min ago · produced by the Infra crew
        </footer>
      </main>
    </div>
  )
}
