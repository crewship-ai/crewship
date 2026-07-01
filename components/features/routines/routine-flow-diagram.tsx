"use client"

import {
  Clock,
  Globe,
  Bot,
  Shuffle,
  Terminal,
  Hourglass,
  Workflow,
  Database,
  Server,
  Send,
  ChevronRight,
  type LucideIcon,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { buildFlowNodes, type FlowNode, type FlowNodeKind, type FlowIconKey, type RoutineManifest } from "@/lib/routine-flow"
import { brandIconByKey, BrandGlyph } from "./brand-icons"

// RoutineFlowDiagram — read-only horizontal "data flow" preview, the
// centerpiece of the routine detail redesign. Renders the node chain from
// buildFlowNodes (trigger → steps → resources → output) as color-coded
// cards joined by → connectors. NOT live — a static derivation of the DSL
// + manifest ("co se kam pošle, náhled, ne živé").

const ICONS: Record<FlowIconKey, LucideIcon> = {
  trigger: Clock,
  http: Globe,
  agent: Bot,
  transform: Shuffle,
  code: Terminal,
  wait: Hourglass,
  call: Workflow,
  "store-redis": Database,
  "store-postgres": Server,
  "store-mysql": Database,
  "store-mongodb": Database,
  store: Database,
  tool: Terminal,
  out: Send,
}

// Per-kind chrome, matching the mockup's node.{trig,agent,store,tool,out}
// classes: trigger=amber, agent=indigo, store=cyan, tool=violet, out=green,
// neutral steps default.
const KIND_STYLE: Record<FlowNodeKind, { node: string; icon: string }> = {
  trigger: { node: "border-amber-500/35 bg-amber-500/[0.07]", icon: "text-amber-400" },
  step: { node: "border-border bg-card", icon: "text-muted-foreground" },
  agent: { node: "border-indigo-500/40 bg-indigo-500/[0.08]", icon: "text-indigo-400" },
  store: { node: "border-cyan-500/35 bg-cyan-500/[0.07]", icon: "text-cyan-400" },
  tool: { node: "border-violet-500/40 bg-violet-500/[0.08]", icon: "text-violet-400" },
  out: { node: "border-emerald-500/35 bg-emerald-500/[0.07]", icon: "text-emerald-400" },
}

function FlowNodeCard({ node }: { node: FlowNode }) {
  // Real brand logo (Postgres/Redis/Ansible/…) when the node resolves to one;
  // otherwise the generic lucide glyph keyed by iconKey, tinted by node kind.
  const brand = brandIconByKey(node.brandIconKey)
  const fallback = ICONS[node.iconKey] ?? Shuffle
  const style = KIND_STYLE[node.kind] ?? KIND_STYLE.step
  return (
    <div
      className={cn(
        "flex w-[112px] shrink-0 flex-col items-center rounded-[10px] border px-2.5 py-2.5 text-center",
        style.node,
      )}
      title={node.detail ? `${node.label} · ${node.detail}` : node.label}
    >
      <BrandGlyph
        brand={brand}
        fallback={fallback}
        className={cn("h-[18px] w-[18px]", !brand && style.icon)}
      />
      <div className="mt-1.5 truncate text-[11px] font-semibold leading-tight text-foreground/90 max-w-full">
        {node.label}
      </div>
      {node.detail && (
        <div className="mt-0.5 line-clamp-2 text-[9.5px] leading-snug text-muted-foreground-soft max-w-full break-words">
          {node.detail}
        </div>
      )}
    </div>
  )
}

export function RoutineFlowDiagram({
  definition,
  manifest,
}: {
  definition: Record<string, unknown> | undefined
  manifest?: RoutineManifest | null
}) {
  const nodes = buildFlowNodes(definition, manifest)

  return (
    <div>
      <div className="flex items-stretch gap-0 overflow-x-auto pb-2.5 pt-1.5">
        {nodes.map((node, i) => (
          <div key={node.id + i} className="flex shrink-0 items-center">
            <FlowNodeCard node={node} />
            {i < nodes.length - 1 && (
              <ChevronRight className="mx-1 h-4 w-4 shrink-0 text-muted-foreground-soft" aria-hidden />
            )}
          </div>
        ))}
      </div>
      <div className="text-[10.5px] text-muted-foreground-soft">
        <span className="text-muted-foreground">Legend:</span>{" "}
        <span className="text-cyan-400">datastore</span> ·{" "}
        <span className="text-violet-400">tool / script</span> ·{" "}
        <span className="text-indigo-400">agent (AI, non-deterministic)</span> · rest deterministic
      </div>
    </div>
  )
}
