"use client"

import { useState } from "react"
import { cn } from "@/lib/utils"
import {
  VariantA, VariantB, VariantC, VariantD, VariantE,
} from "./variants"

const VARIANTS = [
  { id: "A", title: "3-Column: Source Nav + Tree + Preview", desc: "Dedicated source navigator on the left, file tree in the middle, large preview on the right. Best for complex multi-source browsing.", component: VariantA },
  { id: "B", title: "2-Column: Source Tabs + Tree + Preview", desc: "Source tabs at the top, cleaner layout. More horizontal space for the tree and preview.", component: VariantB },
  { id: "C", title: "VS Code Style: Activity Bar + Explorer + Editor", desc: "Familiar IDE-like experience with activity bar, collapsible sections (Agent Output, Container, Crew), git panel, and file search. Most feature-rich.", component: VariantC },
  { id: "D", title: "Minimal: Collapsible Tree + Large Preview", desc: "Simple 2-panel with collapsible file tree. Source quick-switch tabs within the tree. Maximizes preview area.", component: VariantD },
  { id: "E", title: "GitHub Style: Breadcrumb + Table + Drawer", desc: "Navigate directories like GitHub. File table with commit info, size, time. Preview slides in from the right. Familiar for developers.", component: VariantE },
]

export default function PreviewFilesPage() {
  const [activeVariant, setActiveVariant] = useState("A")
  const ActiveComponent = VARIANTS.find((v) => v.id === activeVariant)!.component

  return (
    <div className="max-w-7xl mx-auto py-8 px-6 space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Files Page Redesign</h1>
        <p className="text-sm text-muted-foreground mt-1">Full-page file browser with multi-source navigation, VS Code-style icons, git integration (future), and container browsing.</p>
      </div>

      {/* Variant selector */}
      <div className="flex flex-wrap gap-2">
        {VARIANTS.map((v) => (
          <button
            key={v.id}
            onClick={() => setActiveVariant(v.id)}
            className={cn(
              "px-3 py-1.5 rounded-lg text-xs font-medium transition-colors border",
              activeVariant === v.id ? "bg-primary text-primary-foreground border-primary" : "bg-card border-border text-muted-foreground hover:text-foreground"
            )}
          >
            {v.id}: {v.title.split(":")[0]}
          </button>
        ))}
      </div>

      {/* Active variant description */}
      <div className="bg-card border rounded-lg px-4 py-3">
        <div className="text-sm font-semibold">{VARIANTS.find((v) => v.id === activeVariant)!.title}</div>
        <p className="text-xs text-muted-foreground mt-0.5">{VARIANTS.find((v) => v.id === activeVariant)!.desc}</p>
      </div>

      {/* Mockup */}
      <ActiveComponent />

      {/* Feature comparison */}
      <div className="border rounded-lg overflow-hidden">
        <table className="w-full text-xs">
          <thead>
            <tr className="bg-muted/50">
              <th className="text-left px-4 py-2 font-semibold">Feature</th>
              {VARIANTS.map((v) => <th key={v.id} className="text-center px-3 py-2 font-semibold">{v.id}</th>)}
            </tr>
          </thead>
          <tbody>
            {[
              ["Multi-source navigation", "yes", "yes", "yes", "yes", "yes"],
              ["Recursive file tree", "yes", "yes", "yes", "yes", "no (table)"],
              ["Git integration panel", "no", "no", "yes", "no", "partial"],
              ["File search across files", "filter", "filter", "yes", "no", "no"],
              ["Collapsible sidebar", "no", "no", "no", "yes", "no"],
              ["VS Code-like icons", "yes", "yes", "yes", "yes", "yes"],
              ["Breadcrumb navigation", "no", "no", "yes", "no", "yes"],
              ["Open file tabs", "no", "no", "yes", "no", "no"],
              ["Code preview with syntax", "yes", "yes", "yes", "yes", "yes"],
              ["Space efficiency", "medium", "high", "medium", "high", "high"],
              ["Complexity to implement", "low", "low", "high", "low", "medium"],
            ].map(([feature, ...values], i) => (
              <tr key={i} className="border-t">
                <td className="px-4 py-2 text-muted-foreground">{feature}</td>
                {values.map((v, j) => (
                  <td key={j} className={cn("text-center px-3 py-2", v === "yes" ? "text-emerald-600" : v === "no" ? "text-muted-foreground/50" : "text-amber-600")}>{v}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
