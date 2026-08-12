"use client"

// BrandPicker — the icon a credential wears, and the one control that changes
// it. Used by the add-credential wizard's first step and by the flat
// Add/Edit form.
//
// It was built as a 6-column wall of icon tiles with 8px captions, which read
// well at the ~140 brands the registry then held. The registry is on its way
// past several hundred, and at that size the tile wall stops working: the
// captions are unreadable, the grid is four screens deep, and a tile is a
// 40px square target that a thumb misses. So the body is a LIST now — icon,
// full label, one row per brand — and the design follows from the scale:
//
//   • Search is the fast path, not a filter on a gallery. Results are ranked
//     (exact → label prefix → key prefix → contains → keyword), so the top
//     row is the obvious answer and Enter takes it without touching the mouse.
//   • The list is CAPPED. Painting four hundred identical stripes turns the
//     search box into decoration — you scroll instead of typing. The footer
//     always says how many were held back, so the cap is never silent.
//   • Category chips stay: they are a browsing aid for someone who does not
//     yet know the brand's name. What was rejected was being asked to pick a
//     CATEGORY as a step of creating a credential — this is not that.
//   • Inline `style={{ color }}` because Tailwind cannot express several
//     hundred arbitrary brand hex values.
//
// Auto-detection from the typed name still wins until the user picks by hand;
// the latch lives in the parent form, not here.

import * as React from "react"
import { Search, ChevronDown, Check } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { useIsMobile } from "@/hooks/use-mobile"
import {
  BRAND_REGISTRY,
  BRAND_CATEGORIES,
  GENERIC_BRAND,
  getBrand,
  brandColor,
  type BrandCategory,
  type BrandEntry,
} from "@/lib/credential-providers/registry"
import { cn } from "@/lib/utils"

/**
 * How many rows the list draws at once.
 *
 * Exported because the test asserts against it rather than against a literal:
 * the registry is owned by another surface and grows without asking, and a
 * test that says "166" is a test that fails on somebody else's PR.
 */
export const BRAND_RESULT_CAP = 100

interface BrandPickerProps {
  value: string                    // current provider key
  onChange: (key: string) => void  // user picks a brand → returns the key
  className?: string
}

/**
 * Match rank. Lower is better; -1 is "no match".
 *
 * The order is the order a person expects: something whose NAME starts with
 * what they typed beats something that merely mentions it in a keyword. Without
 * it "git" put DigitalOcean (di-git-alocean) in the same undifferentiated heap
 * as GitHub, and the top row stopped being the answer.
 */
function rank(brand: BrandEntry, q: string): number {
  const label = brand.label.toLowerCase()
  const key = brand.key.toLowerCase()
  if (label === q) return 0
  if (label.startsWith(q)) return 1
  if (key.startsWith(q)) return 2
  if (label.includes(q)) return 3
  if (key.includes(q)) return 4
  const keywords = brand.keywords ?? []
  if (keywords.some((k) => k.toLowerCase().startsWith(q))) return 5
  if (keywords.some((k) => k.toLowerCase().includes(q))) return 6
  return -1
}

export function BrandPicker({ value, onChange, className }: BrandPickerProps) {
  const [open, setOpen] = React.useState(false)
  const [query, setQuery] = React.useState("")
  const [activeCat, setActiveCat] = React.useState<BrandCategory | "All">("All")
  const isMobile = useIsMobile()

  const current = getBrand(value)
  const CurrentIcon = current.Icon

  const q = query.trim().toLowerCase()

  /** Everything that matches, best first. Not yet capped — the count is honest. */
  const matches = React.useMemo(() => {
    const scoped = activeCat === "All"
      ? BRAND_REGISTRY
      : BRAND_REGISTRY.filter((b) => b.category === activeCat)
    if (!q) return scoped
    return scoped
      .map((brand, i) => ({ brand, score: rank(brand, q), i }))
      .filter((m) => m.score >= 0)
      // Registry order is popularity-first within a category, so it is the
      // right tie-break: equal-scoring brands keep the order the catalog
      // already argued for.
      .sort((a, b) => a.score - b.score || a.i - b.i)
      .map((m) => m.brand)
  }, [q, activeCat])

  const visible = matches.slice(0, BRAND_RESULT_CAP)
  const heldBack = matches.length - visible.length

  /**
   * Category headings, but only while browsing. A search result set is ranked
   * by relevance, so slicing it back into categories would fight the ranking;
   * and a list already narrowed to one category does not need a heading that
   * repeats the chip above it.
   */
  const groups = React.useMemo(() => {
    if (q || activeCat !== "All") return [{ category: null as BrandCategory | null, brands: visible }]
    return BRAND_CATEGORIES
      .map((category) => ({ category, brands: visible.filter((b) => b.category === category) }))
      .filter((g) => g.brands.length > 0)
  }, [q, activeCat, visible])

  function pick(key: string) {
    onChange(key)
    setOpen(false)
  }

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        // A picker that reopens on last week's query looks broken. Reset on
        // close rather than on open so the reset is never visible mid-fade.
        if (!next) { setQuery(""); setActiveCat("All") }
      }}
    >
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className={cn("h-9 gap-1.5 px-2 sm:h-8", className)}
          aria-label={`Provider: ${current.label}. Click to change.`}
        >
          <CurrentIcon
            className="h-4 w-4 shrink-0"
            style={{ color: brandColor(current) }}
          />
          <span className="type-meta max-w-[110px] truncate font-normal">
            {current === GENERIC_BRAND ? "No brand" : current.label}
          </span>
          <ChevronDown className="h-3 w-3 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        sideOffset={6}
        collisionPadding={12}
        // Radix focuses the first tabbable child on open, which is the search
        // box — and on a phone that raises the keyboard over the list the user
        // opened to look at. Focus stays on the trigger there; Escape and the
        // dismiss layer work from either place.
        onOpenAutoFocus={(e) => { if (isMobile) e.preventDefault() }}
        data-testid="brand-panel"
        // 26rem is the comfortable two-column width; the clamp is what keeps a
        // 416px panel off a 390px phone, where it would otherwise be the one
        // element that makes the whole page scroll sideways. dvh rather than
        // vh so the on-screen keyboard does not push the footer out of reach.
        className="flex max-h-[min(28rem,70dvh)] w-[min(26rem,calc(100vw-1.5rem))] flex-col p-0"
      >
        <div className="shrink-0 border-b border-hairline p-2">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              // Not on a phone: focusing raises the keyboard over the list the
              // user just opened, so the first thing they see is half a panel.
              // On a pointer device it costs nothing and saves the click.
              autoFocus={!isMobile}
              placeholder="Search brands…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== "Enter") return
                e.preventDefault()
                if (visible[0]) pick(visible[0].key)
              }}
              // No type-size override: the ui kit's own text-base/md:text-sm is
              // already 16px on a phone, and anything under that makes iOS
              // Safari zoom the page the moment the field takes focus.
              className="h-9 pl-7 sm:h-8"
            />
          </div>
        </div>

        {/* Eighteen categories wrap to four rows on a phone, which would push
            the results themselves below the fold. Bounded and scrollable
            keeps the search box, the chips and the first results all visible. */}
        <div
          data-testid="brand-categories"
          className="flex max-h-[4.75rem] shrink-0 flex-wrap gap-1 overflow-y-auto border-b border-hairline px-2 py-1.5"
        >
          <CategoryChip
            label="All"
            active={activeCat === "All"}
            onClick={() => setActiveCat("All")}
          />
          {BRAND_CATEGORIES.map((c) => (
            <CategoryChip
              key={c}
              label={c}
              active={activeCat === c}
              onClick={() => setActiveCat(c)}
            />
          ))}
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-1.5">
          {visible.length === 0 ? (
            <p className="py-8 text-center type-meta text-muted-foreground">
              No brand matches &ldquo;{query}&rdquo;.
              <br />
              <button
                type="button"
                onClick={() => pick("NONE")}
                className="mt-2 inline-block text-primary hover:underline"
              >
                Use generic icon →
              </button>
            </p>
          ) : (
            <div data-testid="brand-results" className="grid grid-cols-1 gap-0.5 sm:grid-cols-2">
              {groups.map((g) => (
                <React.Fragment key={g.category ?? "results"}>
                  {g.category && (
                    <h4 className="type-meta col-span-full px-2 pb-0.5 pt-2 uppercase tracking-wide text-muted-foreground-soft first:pt-0">
                      {g.category}
                    </h4>
                  )}
                  {g.brands.map((b) => (
                    <BrandRow
                      key={b.key}
                      brand={b}
                      selected={b.key === value}
                      onClick={() => pick(b.key)}
                    />
                  ))}
                </React.Fragment>
              ))}
            </div>
          )}
        </div>

        <div className="flex shrink-0 items-center justify-between gap-2 border-t border-hairline px-3 py-2 type-meta text-muted-foreground">
          <span className="min-w-0 truncate">
            {heldBack > 0
              ? `Showing ${visible.length} of ${matches.length} — keep typing to narrow`
              : `${matches.length} brand${matches.length === 1 ? "" : "s"}`}
          </span>
          <button
            type="button"
            onClick={() => pick("NONE")}
            className="shrink-0 hover:text-foreground"
          >
            No brand
          </button>
        </div>
      </PopoverContent>
    </Popover>
  )
}

function CategoryChip({
  label, active, onClick,
}: { label: string; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        "h-6 rounded-full border px-2 type-meta font-medium leading-none transition-colors",
        active
          ? "border-primary/40 bg-primary/20 text-primary"
          : "border-border/60 text-muted-foreground hover:border-border hover:text-foreground",
      )}
    >
      {label}
    </button>
  )
}

function BrandRow({
  brand, selected, onClick,
}: { brand: BrandEntry; selected: boolean; onClick: () => void }) {
  const Icon = brand.Icon
  return (
    <button
      type="button"
      onClick={onClick}
      // Kept as a title because the label truncates in the two-column layout,
      // and because it is the handle the form's tests reach for.
      title={brand.label}
      aria-pressed={selected}
      className={cn(
        "flex min-h-9 w-full items-center gap-2 rounded-md border px-2 py-1.5 text-left transition-colors",
        selected
          ? "border-primary/50 bg-primary/10"
          : "border-transparent hover:border-border/60 hover:bg-surface-raised",
      )}
    >
      <Icon className="h-4 w-4 shrink-0" style={{ color: brandColor(brand) }} />
      <span className="type-meta min-w-0 flex-1 truncate text-foreground">{brand.label}</span>
      {selected && <Check className="h-3.5 w-3.5 shrink-0 text-primary" />}
    </button>
  )
}
