"use client"

import { Activity, Clock, Settings as SettingsIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { isAdminTier } from "@/lib/permissions/tiers"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { CrewAuditSection } from "@/components/features/settings/sections/crew-audit-section"

import { AUDIT_RANGES, AUDIT_SOURCES, DEFAULT_AUDIT_FILTERS, rangeLabel } from "./audit-filters"
import { useAuditFilters } from "./use-audit-filters"

/**
 * Settings › Audit log as a nested page. The side panel picks the trail and
 * the time range — the two choices that decide what the table is — and the
 * table takes the full width, with search, person, type and a custom range
 * above it. Filters live in the URL (useAuditFilters).
 */
export function AuditLogPage() {
  const { workspaceId, role, loading } = useWorkspace()
  // GET /api/v1/audit is ADMIN+; the Settings row is hidden below that, and a
  // typed-in URL gets a sentence instead of a 403.
  const allowed = loading || isAdminTier(role)
  const [filters, setFilters] = useAuditFilters()
  const pickSource = (source: (typeof AUDIT_SOURCES)[number]["value"]) =>
    // A new trail keeps the time range; the other filters belong to the old one.
    setFilters({ ...DEFAULT_AUDIT_FILTERS, source, range: filters.range, from: filters.from, to: filters.to })

  const nav = (
    <>
      <DrillNavSection label="Trail">
        {AUDIT_SOURCES.map((s) => (
          <DrillNavItem key={s.value} selected={filters.source === s.value} onSelect={() => pickSource(s.value)} title={s.hint} label={s.label} />
        ))}
      </DrillNavSection>
      <DrillNavSection label="Time">
        {AUDIT_RANGES.map((r) => (
          <DrillNavItem key={r.value} selected={filters.range === r.value} onSelect={() => setFilters({ range: r.value, from: "", to: "" })} label={r.label} />
        ))}
        {filters.range === "custom" && (
          <DrillNavItem selected onSelect={() => {}} icon={<Clock className="h-3.5 w-3.5 shrink-0 opacity-70" />} label={rangeLabel(filters)} />
        )}
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <div className="-mx-1 flex gap-1 overflow-x-auto px-1" role="tablist" aria-label="Trail">
      {AUDIT_SOURCES.map((s) => (
        <button key={s.value} type="button" role="tab" aria-selected={filters.source === s.value} onClick={() => pickSource(s.value)}
          className={cn("h-8 shrink-0 rounded-md px-3 text-xs font-medium", filters.source === s.value ? "bg-[var(--selection-bg)] text-foreground" : "text-muted-foreground")}>
          {s.label}
        </button>
      ))}
    </div>
  )

  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Audit log"
      icon={Activity}
      description="Every state-changing action, immutably recorded"
      nav={nav}
      mobileNav={mobileNav}
    >
      <div className="p-4 md:p-6">
        {!allowed ? (
          <p className="rounded-card border border-border bg-card px-4 py-6 text-center text-sm text-muted-foreground">
            The audit log is readable by workspace Admins and the Owner.
          </p>
        ) : workspaceId && <CrewAuditSection workspaceId={workspaceId} filters={filters} onFiltersChange={setFilters} showSources={false}
          title="Events"
          description={`${AUDIT_SOURCES.find((s) => s.value === filters.source)?.label ?? ""} trail · ${rangeLabel(filters)}`} />}
      </div>
    </DrillPage>
  )
}
