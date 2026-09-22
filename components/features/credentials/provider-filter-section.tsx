"use client"

import { SidebarRow, SidebarSection } from "@/components/layout/sidebar-kit"
import { LoginBrandMark } from "./provider-login-bits"

export function ProviderFilterSection({ providers, selected, onSelect, active = true }: {
  providers: { value: string; label: string; count: number }[]
  selected: string[]
  onSelect: (key: string) => void
  active?: boolean
}) {
  const options = providers.filter((p) => p.count > 0)
  return <SidebarSection label="Providers" count={providers.reduce((sum, p) => sum + p.count, 0)} className="border-b border-white/[0.06]">
    <SidebarRow selected={active && selected.length === 0} onSelect={() => onSelect("")}>
      <span className="min-w-0 flex-1">All providers</span>
      <span className="type-meta tabular-nums text-muted-foreground">{providers.reduce((sum, p) => sum + p.count, 0)}</span>
    </SidebarRow>
    <div>
      {options.map((p) => <SidebarRow key={p.value} selected={selected.includes(p.value)} onSelect={() => onSelect(p.value)}>
        <LoginBrandMark provider={p.value} size="sm" />
        <span className="min-w-0 flex-1 truncate">{p.label}</span>
        <span className="type-meta tabular-nums text-muted-foreground" aria-label={`${p.count} connected accounts`}>{p.count}</span>
      </SidebarRow>)}
    </div>
  </SidebarSection>
}
